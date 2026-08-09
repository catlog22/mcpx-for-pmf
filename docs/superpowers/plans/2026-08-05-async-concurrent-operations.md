# 异步并发操作实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 `executing-plans` 逐任务实现此计划。步骤使用复选框（`- [ ]`）跟踪进度。

**目标：** 为 MCPX 公开工具增加显式异步执行、批量 DAG 调度和统一的操作查询、结果读取、取消与确认恢复能力。

**架构：** 新增独立的 `internal/operation` 调度域，使用 SQLite 持久化操作和步骤状态，使用进程级 worker pool 与工作区 `RWMutex` 控制并发。Runtime 负责将公开工具请求转换为操作步骤，并通过带操作上下文的已注册工具处理器执行；观测层记录操作、步骤和实际工具调用的父子关系。

**技术栈：** Go 标准库 `context`、`sync`、`encoding/json`，现有 `database/sql` + SQLite，现有 MCPX ARC Envelope、Observation Store 和 Task Manager。

---

## 文件清单

### 新建

- `internal/operation/types.go`：操作、步骤、状态和调度事件的领域类型。
- `internal/operation/service.go`：SQLite 持久化、worker pool、依赖调度、取消和等待。
- `internal/operation/service_test.go`：调度顺序、并发、失败传播和取消测试。
- `internal/server/operation_runtime.go`：Runtime 与操作服务的适配、工具步骤执行和结果编码。
- `internal/server/tools_operation.go`：`operation_batch`、`operation_manage` 处理器。
- `internal/server/tools_operation_test.go`：公开异步接口与批量操作测试。

### 修改

- `internal/state/migrations.go`：增加 `operations`、`operation_steps` 表及索引。
- `internal/state/retention.go`：按过期时间清理已完成且已关闭会话的操作结果。
- `internal/envelope/envelope.go`：解析并过滤 `execution_mode`，保留操作上下文字段。
- `internal/server/runtime_context.go`：增加操作 ID、父操作 ID 和步骤 ID 上下文。
- `internal/server/runtime.go`：Runtime 持有操作服务和工具处理器索引，初始化并关闭调度器。
- `internal/server/observability.go`：在工具生命周期前后接入异步调度，子操作跳过二次异步包装。
- `internal/server/tools_catalog.go`：给公开工具增加 `execution_mode`，注册两个操作工具。
- `internal/server/tools_public_adapters.go`：增加操作状态、结果和错误的公开响应适配。
- `internal/observation/event.go`：增加 `StepID` 和操作事件类型。
- `internal/observation/store.go`：持久化和读取 `step_id`。
- `internal/server/observation_bridge.go`：将 Runtime 操作上下文写入观测事件。
- `internal/server/acceptance_protocol_test.go`、`internal/server/public_catalog_test.go`：更新工具目录与公开 schema 断言。
- `internal/server/runtime_context_test.go`、`internal/server/observer_integration_test.go`：覆盖操作上下文和父子观测链路。

实现文档保留在 `docs/`，不纳入实现 commit。

## 任务 1：建立操作持久化模型和状态机

**文件：**
- 创建：`internal/operation/types.go`
- 创建：`internal/operation/service.go`
- 创建：`internal/operation/service_test.go`
- 修改：`internal/state/migrations.go`

- [x] **步骤 1：先增加持久化表迁移**

在 `internal/state/migrations.go` 的 `migrations` 末尾增加一个迁移，创建：

```sql
CREATE TABLE IF NOT EXISTS operations (
    id TEXT PRIMARY KEY,
    remote_session_id TEXT NOT NULL,
    workspace_name TEXT NOT NULL,
    request_id TEXT NOT NULL,
    purpose TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','waiting_confirmation','interrupted','cancelled')),
    result_json TEXT NOT NULL DEFAULT '{}',
    error_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    completed_at INTEGER,
    expires_at INTEGER NOT NULL,
    FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS operation_steps (
    operation_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    arguments_json TEXT NOT NULL DEFAULT '{}',
    depends_on_json TEXT NOT NULL DEFAULT '[]',
    exclusive INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','waiting_confirmation','interrupted','cancelled','skipped')),
    request_id TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '{}',
    error_json TEXT NOT NULL DEFAULT '{}',
    confirmation_token TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    completed_at INTEGER,
    PRIMARY KEY (operation_id, step_id),
    FOREIGN KEY (operation_id) REFERENCES operations(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_operations_session_created
    ON operations(remote_session_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operations_state
    ON operations(state, created_at);
CREATE INDEX IF NOT EXISTS idx_operation_steps_state
    ON operation_steps(operation_id, state, step_id);
```

运行：`rtk go test ./internal/state -count=1`

预期：状态迁移测试通过，`schema_migrations` 的最大版本等于 `len(migrations)`。

- [x] **步骤 2：定义操作领域类型**

在 `internal/operation/types.go` 定义以下公开类型，状态字符串必须与迁移约束一致：

```go
type State string

const (
	StateQueued              State = "queued"
	StateRunning             State = "running"
	StateSucceeded           State = "succeeded"
	StateFailed              State = "failed"
	StateWaitingConfirmation State = "waiting_confirmation"
	StateInterrupted         State = "interrupted"
	StateCancelled           State = "cancelled"
	StateSkipped             State = "skipped"
)

type StepSpec struct {
	ID        string
	Tool      string
	Arguments map[string]any
	DependsOn []string
	Exclusive bool
}

type SubmitSpec struct {
	ID              string
	RemoteSessionID string
	WorkspaceName   string
	RequestID       string
	Purpose         string
	Steps           []StepSpec
}

type Record struct {
	ID              string
	RemoteSessionID string
	WorkspaceName   string
	RequestID       string
	Purpose         string
	State           State
	Result          json.RawMessage
	Error           json.RawMessage
	Steps           []StepRecord
}

type StepRecord struct {
	ID, Tool, RequestID string
	Arguments          map[string]any
	DependsOn          []string
	Exclusive          bool
	State              State
	Result, Error      json.RawMessage
	ConfirmationToken  string
}

type ResultPage struct {
	Operation  Record
	StepID     string
	Result     json.RawMessage
	NextCursor string
}

type ExecuteInput struct {
	OperationID     string
	StepID          string
	RequestID       string
	RemoteSessionID string
	WorkspaceName   string
	Purpose         string
	Tool            string
	Arguments       map[string]any
}

type ExecuteResult struct {
	Result             json.RawMessage
	WaitingConfirmation bool
	ConfirmationToken  string
	Err                error
}

type Executor func(context.Context, ExecuteInput) ExecuteResult
type EventSink func(Event)
```

`StepSpec.Exclusive` 由 Runtime 根据工具注解计算；操作包不读取 MCP schema，保持调度器与公开工具解耦。

- [x] **步骤 3：编写状态机失败测试**

在 `internal/operation/service_test.go` 增加测试：

```go
func TestServiceRunsIndependentStepsConcurrentlyAndHonorsDependencies(t *testing.T) { /* use barrier channels and assert elapsed order */ }
func TestServiceSkipsDescendantsAfterFailure(t *testing.T) { /* failing parent must skip child */ }
func TestServiceCancelsQueuedAndRunningSteps(t *testing.T) { /* queued never executes; running receives context cancellation */ }
```

测试必须使用 `t.TempDir()` 打开独立 SQLite 数据库，并在 executor 中记录执行开始和结束，不能使用固定 sleep 判断并发。

- [x] **步骤 4：实现 Service 的提交、查询和依赖调度**

在 `internal/operation/service.go` 实现：

```go
type Service struct {
	db             *sql.DB
	now            func() time.Time
	mu             sync.Mutex
	active         map[string]*activeOperation
	jobs           chan stepJob
	workspaceLocks map[string]*sync.RWMutex
	sink           EventSink
	stop           chan struct{}
	closed         chan struct{}
	wg             sync.WaitGroup
}

type activeOperation struct {
	done       chan struct{}
	cancel     context.CancelFunc
	stepCancel map[string]context.CancelFunc
}

type stepJob struct {
	operationID string
	stepID      string
	executor    Executor
}

func New(db *sql.DB, workers int, sink EventSink) (*Service, error)
func (s *Service) Submit(ctx context.Context, spec SubmitSpec, executor Executor) (Record, error)
func (s *Service) Get(ctx context.Context, operationID string) (Record, error)
func (s *Service) Wait(ctx context.Context, operationID string, timeout time.Duration) (Record, bool, error)
func (s *Service) Result(ctx context.Context, operationID, stepID, cursor string, limit int) (ResultPage, error)
func (s *Service) Cancel(ctx context.Context, operationID string) (Record, error)
func (s *Service) Resume(ctx context.Context, operationID, stepID, confirmationToken string, executor Executor) (Record, error)
func (s *Service) Close() error
```

实现要求：

- 提交事务一次写入 `operations` 和所有 `operation_steps`，任一校验失败不得写入半个批次。
- 验证步骤 ID 唯一、工具非空、依赖存在、依赖无环，至少支持 1 个步骤，批次最多 32 个步骤。
- 每个操作拥有完成通知；`Wait` 超时只返回当前记录，不触发取消。
- 就绪步骤进入 worker queue；步骤完成后重新计算同一操作中可运行的后继步骤。
- `Exclusive=false` 使用工作区 `RLock`，`Exclusive=true` 使用工作区 `Lock`；进程级 worker 数量限制总并发。
- 独立分支失败后继续调度无依赖分支；依赖失败的步骤标记 `skipped`。
- 终端状态通过 SQLite 更新，结果读取不重新执行操作。
- 启动时把上次遗留的 `queued`、`running` 标记为 `interrupted`；`waiting_confirmation` 保留以支持恢复。

运行：`rtk go test ./internal/operation ./internal/state -count=1`

预期：操作服务测试全部通过，覆盖并发、依赖、失败、取消和重启恢复。

## 任务 2：传递操作上下文并扩展观测事件

**文件：**
- 修改：`internal/envelope/envelope.go`
- 修改：`internal/server/runtime_context.go`
- 修改：`internal/server/runtime.go`
- 修改：`internal/observation/event.go`
- 修改：`internal/observation/store.go`
- 修改：`internal/server/observation_bridge.go`
- 修改：`internal/state/migrations.go`
- 测试：`internal/server/runtime_context_test.go`、`internal/server/observer_integration_test.go`

- [x] **步骤 1：增加请求和 Runtime 操作字段**

给 `envelope.Request` 增加 `ParentOperationID` 和 `StepID`；给 `RuntimeContext` 增加 `OperationID`、`ParentOperationID`、`StepID`。`parseEnv` 的优先级为：

1. 运行时上下文中的操作字段。
2. 由 Request ID 推导的普通 `op_` ID。

`execution_mode` 作为运行时控制字段从 `Payload` 移除，不能被业务 handler 当作业务参数处理。

- [x] **步骤 2：扩展 Observation Event 与 SQLite 迁移**

给 `observation.Event` 增加：

```go
StepID string `json:"step_id,omitempty"`
```

增加 `ALTER TABLE observation_events ADD COLUMN step_id TEXT NOT NULL DEFAULT '';`，并在 `Store.Append`、`List`、`History`、`scanEvents` 的 SQL 顺序中保持字段一致。

增加事件类型：

```go
TypeOperationStarted   = "operation.started"
TypeOperationStepStarted = "operation.step.started"
TypeOperationStepCompleted = "operation.step.completed"
TypeOperationCompleted = "operation.completed"
```

- [x] **步骤 3：编写观测上下文测试**

验证带有 `OperationID=op_1`、`ParentOperationID=op_parent`、`StepID=read_a` 的工具调用写入数据库后，历史查询返回三个字段，并且并发事件的 sequence 仍然单调递增。

运行：`rtk go test ./internal/observation ./internal/server -run 'Operation|Observer|RuntimeContext' -count=1`

## 任务 3：接入 Runtime 与工具执行包装

**文件：**
- 创建：`internal/server/operation_runtime.go`
- 修改：`internal/server/runtime.go`
- 修改：`internal/server/observability.go`
- 修改：`internal/server/runtime_context.go`
- 修改：`internal/envelope/envelope.go`
- 测试：`internal/server/observability_regression_test.go`

- [x] **步骤 1：为 Runtime 增加操作服务和工具索引**

在 `Runtime` 增加：

```go
operations  *operation.Service
toolHandlers map[string]mcpserver.ToolHandlerFunc
toolMeta     map[string]toolAnnotation
```

`addTool` 注册时同时保存原始 handler 和工具注解；`New` 在 `stateStore.DB()` 初始化操作服务，`Close` 在任务管理器关闭前停止 worker 并等待已启动步骤写入最终状态。

- [x] **步骤 2：实现 Runtime 操作执行适配器**

在 `operation_runtime.go` 实现：

```go
func (r *Runtime) executeOperationStep(ctx context.Context, input operation.ExecuteInput) operation.ExecuteResult
func (r *Runtime) submitAsyncTool(ctx context.Context, name string, req mcp.CallToolRequest, env envelope.Request) (*mcp.CallToolResult, error)
func (r *Runtime) operationChildContext(ctx context.Context, input operation.ExecuteInput) context.Context
func operationResult(result *mcp.CallToolResult, callErr error) (json.RawMessage, operation.ExecuteResult)
```

子操作请求必须：

- 使用父请求的认证和会话上下文，但使用独立的步骤 Request ID。
- 继承 `session_id` 和 `purpose`，只覆盖工具业务参数。
- 设置 `OperationID`、`ParentOperationID` 和 `StepID`。
- 添加内部 child 标记，使 `instrumentTool` 不再次创建异步操作。
- 通过已注册的 instrumented handler 执行，确保 Skill、MCP、命令和 Task 仍由现有观测链路记录。

- [x] **步骤 3：修改 instrumentTool 的异步分流**

在现有 `instrumentTool` 中保持工具开始、执行、完成和 ARC 包装流程，只在调用 handler 前增加：

```go
if !isOperationChild(ctx) && name != "operation_batch" && name != "operation_manage" && executionMode(req) == "async" {
	result, err = r.submitAsyncTool(callCtx, name, req, observationRequest)
} else {
	result, err = handler(callCtx, req)
}
```

异步提交本身仍写入外层公开工具的 `tool.started` / `tool.completed`，后台子操作另写真实工具调用事件。客户端断开不能取消后台操作，后台上下文使用 `context.WithoutCancel`，但 `cancel` 操作必须显式传递取消信号。

运行：`rtk go test ./internal/server -run 'Observability|RuntimeContext' -count=1`

## 任务 4：注册公开 schema 和操作管理器

**文件：**
- 修改：`internal/server/tools_catalog.go`
- 创建：`internal/server/tools_operation.go`
- 修改：`internal/server/tools_public_adapters.go`
- 测试：`internal/server/tools_operation_test.go`、`internal/server/public_catalog_test.go`、`internal/server/acceptance_protocol_test.go`

- [x] **步骤 1：给现有公开工具加入 `execution_mode`**

在 `publicTool` 中自动加入：

```go
"execution_mode": enumSchema("执行模式", "sync", "async")
```

不把它加入 required；`workspace_list`、`session_open`、`operation_batch` 和 `operation_manage` 不允许进入后台异步包装，其他需要 Remote Session 的公开工具才接受 `execution_mode=async`。

- [x] **步骤 2：注册 `operation_batch` schema**

增加严格 schema：

```go
operations := arraySchema(map[string]any{
	"type": "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"id": stringSchema("批次内唯一的步骤 ID"),
		"tool": stringSchema("已注册的公开工具名称"),
		"arguments": map[string]any{"type": "object", "additionalProperties": true},
		"depends_on": arraySchema(map[string]any{"type": "string"}, "前置步骤 ID"),
	},
	"required": []string{"id", "tool", "arguments"},
}, "带依赖关系的公开工具操作")
```

注册时要求 `session_id`、`purpose`、`operations`；批量操作最大 32 步由 handler 和 service 双重校验。

- [x] **步骤 3：注册 `operation_manage` schema**

字段：`session_id`、`operation_id`、`action`、`step_id`、`timeout_ms`、`confirmation_token`、`cursor`、`limit`。`action` 枚举为 `status`、`wait`、`result`、`cancel`、`resume`。

- [x] **步骤 4：实现公开 handler**

`toolOperationBatch`：

1. 调用 `changeRequest` 验证认证、会话和会话角色。
2. 从 payload 解析子步骤并查找工具注册表。
3. 将继承的 `session_id`、`purpose` 合并到子参数，按工具的 `RawInputSchema` 校验 required 字段和 `additionalProperties=false` 的未知字段。
4. 根据 `toolMeta[tool].ReadOnly` 和 `OpenWorld` 计算 `Exclusive`。
5. 调用 `operations.Submit`，返回 `accepted`、`operation_id` 和 `state`。

批次不得包含 `operation_batch`、`operation_manage` 或 `secret_provide`；子工具的权限、确认和参数校验仍由实际工具 handler 再次执行。

批量预校验必须在任何步骤入队前完成；schema 校验失败时返回具体的 `step_id`、工具名和字段名，不能启动部分批次。

`toolOperationManage`：

1. 先按 `session_id` 调用 `remote.Get`，确认调用方属于目标会话。
2. `status` 调用 `Get`，`wait` 调用 `Wait`，`result` 调用 `Result`。
3. `cancel` 和 `resume` 调用对应服务方法。
4. 将内部状态映射为既有公开状态，并通过 `remoteResult` / `terminalError` 返回。

运行：`rtk go test ./internal/server -run 'Operation|Catalog|Acceptance' -count=1`

## 任务 5：实现确认、取消和结果读取语义

**文件：**
- 修改：`internal/operation/service.go`
- 修改：`internal/server/operation_runtime.go`
- 修改：`internal/server/tools_operation.go`
- 修改：`internal/server/tools_command_execute.go`
- 测试：`internal/server/tools_operation_test.go`、`internal/approval/*_test.go`

- [x] **步骤 1：识别子操作的等待确认结果**

执行适配器解析工具返回的 ARC Envelope；当 `status` 为 `waiting_confirmation` 时，把响应中的 `confirmation_token` 写入步骤，步骤状态改为 `waiting_confirmation`，不把它视为普通失败。

- [x] **步骤 2：实现 `resume`**

`Resume` 必须验证：

- 操作属于当前会话。
- 步骤当前状态为 `waiting_confirmation`。
- 传入令牌与步骤记录完全一致。
- 原始业务参数摘要没有变化。

验证通过后，将令牌注入本次子操作请求，仅执行该步骤及其已满足依赖的后继步骤；令牌不写入公开观测输入、不作为认证凭证。

- [x] **步骤 3：实现取消和大结果读取**

每个运行步骤保存 `context.CancelFunc`；取消操作时先阻止新的就绪步骤，再取消运行中的步骤。`Result` 对 `result_json` 做有界 UTF-8 分页，返回 `cursor`，不得重新执行步骤。

- [x] **步骤 4：增加行为测试**

覆盖：

```go
func TestOperationAsyncReturnsIDAndWaitReturnsResult(t *testing.T)
func TestOperationBatchRunsIndependentStepsInParallel(t *testing.T)
func TestOperationBatchPropagatesFailureAndSkipsDescendants(t *testing.T)
func TestOperationResumeRequiresMatchingConfirmationToken(t *testing.T)
func TestOperationCancelStopsRunningCommandStep(t *testing.T)
func TestOperationResultIsIdempotentAndPaginated(t *testing.T)
```

## 任务 6：补齐端到端观测与历史查询

**文件：**
- 修改：`internal/server/operation_runtime.go`
- 修改：`internal/server/observation_bridge.go`
- 修改：`internal/server/tools_public_adapters.go`
- 修改：`internal/observation/render.go`
- 修改：`internal/observation/timeline.go`
- 测试：`internal/server/observer_integration_test.go`、`internal/observation/history_test.go`

- [x] **步骤 1：发送操作级观测事件**

在提交、步骤进入运行、步骤完成、操作完成和操作取消时发送事件，事件必须包含 `operation_id`；步骤事件额外包含 `step_id`，实际工具事件包含 `parent_operation_id`。

- [x] **步骤 2：扩展历史视图**

`historyEventView` 返回 `step_id`；`workspace_history_read` 的 `operation_ids` 查询同时能命中操作级事件和工具级事件，保持现有 ID、时间、关键词过滤行为。

- [x] **步骤 3：验证实际调用链**

端到端测试提交一个批次，其中包含 `source_read`、`command_run`、`skill_call` 或 `mcp_call` 的可测试 handler，验证历史中能够按 `operation_id` 找到：

- 操作开始和完成。
- 每个步骤开始和完成。
- 实际公开工具名称。
- 命令、Skill、MCP Server 和 MCP Tool 字段。
- 父操作 ID 和步骤 ID。

运行：`rtk go test ./internal/observation ./internal/server -run 'History|Observer|Operation' -count=1`

## 任务 7：完整验证与计划自检

- [x] **步骤 1：检查格式和静态问题**

运行：

```bash
rtk gofmt -w internal/operation internal/server internal/envelope internal/observation internal/state
rtk git diff --check
test -z "$(gofmt -l ./cmd ./internal)"
rtk go vet ./...
```

预期：无格式差异、无 `vet` 错误。

- [x] **步骤 2：运行分层测试**

运行：

```bash
rtk go test ./internal/operation ./internal/state ./internal/observation ./internal/server -count=1
rtk go test ./... -count=1
```

预期：所有测试通过，且公开工具目录总数为 30。

- [x] **步骤 3：运行竞态测试和构建**

运行：

```bash
rtk go test -race ./... -count=1
rtk go build -o bin/mcpx-server ./cmd/mcpx-server
```

预期：无数据竞争，构建成功。

- [x] **步骤 4：执行规格覆盖自检**

逐项核对设计规格：

- `execution_mode` 明确同步和异步行为。
- `operation_batch` 校验依赖、并发、失败传播和结果获取。
- `operation_manage` 支持状态、等待、结果、取消和恢复。
- 同工作区写操作独占，不同工作区可并发。
- `confirmation_token` 只承担语义确认。
- 操作、步骤、工具、命令、Skill、MCP 观测链路完整。
- 重启不会自动重放未安全恢复的写操作。

发现遗漏时先修正实现和测试，再重新运行本任务的验证命令。
