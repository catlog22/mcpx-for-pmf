# Workspace 实时观测实现计划

> **面向 AI 代理的工作者：** 执行本计划时使用 `subagent-driven-development` 或 `executing-plans` 子技能逐任务实现。每个任务使用复选框跟踪，并在用户确认后提交；本仓库规则禁止代理自动 commit/push。

**目标：** 增加 `mcp-server workspace <workspace name>` 只读观测命令，通过本机 Unix Socket 实时展示 workspace 下全部 Remote Session 的 intent、工具输入输出、命令日志和文件变更。

**架构：** 在 SQLite 中新增独立观察事件表，由服务端统一的 `instrumentTool`、Remote Session 事件回调和 Terminal Task 输出回调生成事件。事件持久化成功后进入内存 Broker，再通过本机 Unix Socket 推送给 CLI；CLI 启动时回放历史，之后按全局 sequence 接收实时事件并在断线后补偿。SQLite 只负责历史和恢复，不负责实时轮询。

**技术栈：** Go 标准库 `encoding/json`、`net.Unix`、`os/signal`、`text/template`/`strings`；现有 `modernc.org/sqlite`、MCPX `state.Store`、MCP-Go 工具 schema、Terminal Task 和 Changeset 服务。

---

## 文件清单

### 新建

- `internal/observation/event.go`：观察事件、订阅请求、事件类型和大小限制。
- `internal/observation/store.go`：SQLite 观察事件的追加、查询和序列号恢复。
- `internal/observation/redact.go`：递归脱敏、文本截断、MCP 输入输出规范化。
- `internal/observation/broker.go`：workspace 过滤、订阅、缓冲和 gap 通知。
- `internal/observation/socket.go`：Unix Socket JSON Lines 协议服务端与生命周期。
- `internal/observation/client.go`：CLI 连接、订阅、断线重连和 sequence 去重。
- `internal/observation/render.go`：text/JSON 事件渲染，包含工具、命令、文件和 Diff 展示。
- `internal/observation/event_test.go`、`store_test.go`、`redact_test.go`、`broker_test.go`、`socket_test.go`、`render_test.go`：观察层单元和协议测试。
- `cmd/mcpx-server/workspace.go`：`workspace` 子命令参数解析、信号处理和观测客户端启动。
- `cmd/mcpx-server/workspace_test.go`：子命令参数和退出行为测试。
- `internal/server/observer_integration_test.go`：Runtime、工具、Task、Changeset 和 Socket 的集成测试。

### 修改

- `internal/state/migrations.go`：新增 `observation_events` 表和 workspace/sequence 索引。
- `internal/envelope/envelope.go`、`internal/envelope/envelope_test.go`：增加顶层 `Intent` 字段及解析测试。
- `internal/server/tools_remote_session.go`：在统一请求入口强制校验 intent。
- `internal/server/tools_catalog.go`：所有公开工具 schema 增加必填 intent。
- `internal/server/agent_guidance.go`、相关测试：声明 intent 请求契约和用户可见响应要求。
- `internal/server/observability.go`：在工具开始和结束边界生成观察事件。
- `internal/server/runtime.go`：初始化观察服务、绑定 Task/Remote Session 回调、启动和关闭 Socket。
- `internal/remotesession/service.go`、相关测试：提供可选的 Remote Session 事件观察回调，并返回事件 sequence。
- `internal/terminal/task.go`、相关测试：增加非阻塞输出观察回调，发送 stdout/stderr 增量和 offset。
- `internal/server/tools_change_execute.go`、`internal/server/tools_changeset.go`：在实际应用 Changeset 后生成逐文件 `file.changed` 事件。
- `cmd/mcpx-server/main.go`：注册 `workspace` 子命令和帮助信息。
- `README.md`：补充 intent 契约、观测命令、输出示例、脱敏和限制。

## 任务 1：建立观察事件持久化和脱敏基础

**文件：**

- 创建：`internal/observation/event.go`、`internal/observation/store.go`、`internal/observation/redact.go`
- 创建：`internal/observation/event_test.go`、`internal/observation/store_test.go`、`internal/observation/redact_test.go`
- 修改：`internal/state/migrations.go`

- [ ] **步骤 1：编写事件和脱敏失败测试**

测试覆盖：

```go
func TestSanitizeRedactsSensitiveKeysAndBoundsText(t *testing.T) {
	value := map[string]any{
		"token": "secret-value",
		"nested": map[string]any{"password": "p"},
		"safe": "visible",
	}
	got, truncated := Sanitize(value, 64)
	if truncated {
		t.Fatal("small sanitized value must not be truncated")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "\"p\"") {
		t.Fatalf("sensitive values leaked: %s", encoded)
	}
}

func TestStoreListFiltersWorkspaceAndSequence(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db)
	first, err := store.Append(context.Background(), Event{Workspace: "mcpx", Type: "tool.started", Intent: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.Append(context.Background(), Event{Workspace: "other", Type: "tool.started", Intent: "ignore"})
	got, err := store.List(context.Background(), "mcpx", first.Sequence-1, 10)
	if err != nil || len(got) != 1 || got[0].Sequence != first.Sequence {
		t.Fatalf("events=%+v err=%v", got, err)
	}
}
```

- [ ] **步骤 2：运行失败测试确认缺少实现**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./internal/observation -run 'TestSanitize|TestStoreList' -count=1
```

预期：FAIL，出现 `undefined: Sanitize`、`undefined: NewStore` 或同等的缺失实现错误。

- [ ] **步骤 3：增加 migration 和事件存储**

在 `migrations` 末尾增加独立 migration，表结构至少包含：

```sql
CREATE TABLE IF NOT EXISTS observation_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_name TEXT NOT NULL,
    remote_session_id TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    operation_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    intent TEXT NOT NULL DEFAULT '',
    input_json TEXT NOT NULL DEFAULT '{}',
    output_json TEXT NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    resource_uri TEXT NOT NULL DEFAULT '',
    stream TEXT NOT NULL DEFAULT '',
    stream_offset INTEGER NOT NULL DEFAULT 0,
    truncated INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_events_workspace_sequence
    ON observation_events(workspace_name, sequence);
```

`Store.Append` 必须使用 `LastInsertId` 返回全局 sequence；`Store.List` 使用 `workspace_name = ? AND sequence > ? ORDER BY sequence ASC LIMIT ?`，limit 默认 100、上限 200。

事件包导出 `MaxEventBytes` 作为单个观察事件输出上限，Store 不接受超过该上限的未脱敏内容。

`Event` 至少定义以下字段：

```go
type Event struct {
	Sequence        int64           `json:"sequence"`
	Workspace       string          `json:"workspace"`
	RemoteSessionID string          `json:"remote_session_id,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	OperationID     string          `json:"operation_id,omitempty"`
	Tool            string          `json:"tool,omitempty"`
	Type            string          `json:"type"`
	Intent          string          `json:"intent,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	ResourceURI     string          `json:"resource_uri,omitempty"`
	Stream          string          `json:"stream,omitempty"`
	Offset          int64           `json:"offset,omitempty"`
	Truncated       bool            `json:"truncated,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}
```

- [ ] **步骤 4：实现脱敏和 UTF-8 安全截断**

实现以下接口，所有输入输出在进入 Store 前先经过它们：

```go
func Sanitize(value any, maxBytes int) (clean any, truncated bool)
func SanitizeText(value string, maxBytes int) (clean string, truncated bool)
func SanitizeJSON(value json.RawMessage, maxBytes int) (clean json.RawMessage, truncated bool)
func NormalizeToolInput(arguments map[string]any, maxBytes int) (json.RawMessage, bool)
func NormalizeToolOutput(result *mcp.CallToolResult, err error, maxBytes int) (json.RawMessage, bool)
```

键名匹配 `token`、`secret`、`password`、`authorization`、`cookie`、`api_key`、`private_key` 等字段时替换为 `[REDACTED]`；图片和二进制内容只保留类型、MIME 和大小，不保存 base64 原文；文本截断不能切断 UTF-8 字符，并设置 `truncated`。

- [ ] **步骤 5：运行观察层测试确认通过**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./internal/observation ./internal/state -count=1
```

预期：PASS；migration 只在测试数据库中新增观察表，既有表数据可正常读取。

## 任务 2：实现 Broker 和本机 Unix Socket 推送

**文件：**

- 创建：`internal/observation/broker.go`、`internal/observation/socket.go`、`internal/observation/client.go`
- 创建：`internal/observation/broker_test.go`、`internal/observation/socket_test.go`
- 修改：`internal/observation/event.go`

- [ ] **步骤 1：编写订阅顺序、过滤和重连测试**

测试必须验证：

- 不同 workspace 的事件不会互相收到。
- 同一 workspace 按 sequence 顺序推送。
- Subscriber 缓冲满时收到 gap 标记而不是静默丢弃。
- Socket 客户端能回放历史并收到后续事件。
- 客户端重复收到同一 sequence 时只渲染一次。

- [ ] **步骤 2：实现内存 Broker**

定义接口：

```go
type Broker struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]*Subscription
}

type Subscription struct {
	Events <-chan Event
	Gaps   <-chan Gap
	Close  func()
}

func (b *Broker) Subscribe(workspace string, buffer int) Subscription
func (b *Broker) Publish(event Event)
func (b *Broker) Close()
```

`Publish` 只投递给 workspace 相同的订阅者。订阅者处理不过来时记录最小和最大缺口 sequence，发送 `Gap`，之后由客户端按 sequence 请求历史补偿。

- [ ] **步骤 3：实现 Socket JSON Lines 协议**

客户端首帧：

```json
{"type":"subscribe","workspace":"mcpx","after_sequence":0,"history_limit":100,"format":"text"}
```

服务端帧：

```json
{"type":"hello","workspace":"mcpx","observer_id":"obs_xxx"}
{"type":"event","event":{}}
{"type":"gap","from_sequence":12,"to_sequence":15}
{"type":"heartbeat","sequence":15}
{"type":"error","code":"WORKSPACE_NOT_FOUND","message":"..."}
```

服务端流程必须先注册 Broker 订阅，再确定历史查询水位；先发送历史，再发送水位后的事件。`SocketServer.Close` 关闭 listener、所有连接和 Broker 订阅，并清理自己创建的 Socket 文件。

Unix 实现使用 `net.Listen("unix", path)`，运行目录创建为 `0700`，Socket 文件设置为 `0600`。非 Unix 构建提供明确的 `observer transport is unsupported` 错误，不影响普通 MCP HTTP 服务构建。

- [ ] **步骤 4：实现客户端重连和 sequence 去重**

客户端保存 `lastSequence`，断线后重新发送：

```go
request.AfterSequence = lastSequence
request.HistoryLimit = 100
```

收到事件时只接受 `Sequence > lastSequence`；遇到 gap 先请求补偿，补偿完成后再继续 live channel。

- [ ] **步骤 5：运行协议测试**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./internal/observation -run 'TestBroker|TestSocket|TestClient' -count=1
```

预期：PASS；测试结束后临时 Socket、连接和 goroutine 全部关闭。

## 任务 3：强制 intent 并更新工具 schema

**文件：**

- 修改：`internal/envelope/envelope.go`、`internal/envelope/envelope_test.go`
- 修改：`internal/server/tools_remote_session.go`、`internal/server/tools_catalog.go`
- 修改：`internal/server/agent_guidance.go` 及其测试
- 修改：现有 `internal/server/*_test.go` 中直接构造 `mcp.CallToolRequest` 的测试夹具

- [ ] **步骤 1：编写 intent 失败测试**

增加以下行为测试：

```go
func TestParseRequestKeepsIntentOutOfPayload(t *testing.T) {
	req, err := envelope.ParseRequest(json.RawMessage(`{"intent":"inspect","workspace":"demo","query":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Intent != "inspect" {
		t.Fatalf("intent=%q", req.Intent)
	}
	if _, exists := req.Payload["intent"]; exists {
		t.Fatalf("intent leaked into payload: %+v", req.Payload)
	}
}

func TestRemoteRequestRejectsMissingIntent(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"workspace": "demo"}
	_, _, failure := rt.remoteRequest(context.Background(), req)
	if failure == nil {
		t.Fatal("missing intent was accepted")
	}
	response := decodeToolResult(t, failure)
	if errorCode(response) != "intent_required" {
		t.Fatalf("error=%+v", response)
	}
}

func TestRemoteRequestRejectsOversizedIntent(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"intent": strings.Repeat("x", 513), "workspace": "demo"}
	_, _, failure := rt.remoteRequest(context.Background(), req)
	if failure == nil {
		t.Fatal("oversized intent was accepted")
	}
}

func TestEveryRegisteredToolRequiresIntent(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcpserver.NewMCPServer("mcpx-test", "1")
	rt.registerTools(protocol)
	for name, registered := range protocol.ListTools() {
		if !containsString(registered.Tool.InputSchema.Required, "intent") {
			t.Errorf("tool %q does not require intent: %+v", name, registered.Tool.InputSchema.Required)
		}
	}
}
```

将已有合法测试请求通过 `callEnvelope` 统一注入 `intent: "test operation"`；专门验证缺失 intent 的测试绕过该辅助函数并明确保留空值。

- [ ] **步骤 2：扩展 envelope 和统一校验**

在 `envelope.Request` 增加：

```go
Intent string `json:"intent,omitempty"`
```

在 `ParseRequest` 的 flat argument 合并逻辑中跳过 `intent`，避免它进入 `Payload`。在 `remoteRequest` 完成认证后调用：

```go
func requireIntent(req envelope.Request) error {
	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		return fmt.Errorf("intent is required")
	}
	if len(intent) > 512 {
		return fmt.Errorf("intent exceeds 512 bytes")
	}
	return nil
}
```

校验失败返回 `INTENT_REQUIRED` 或稳定的参数校验错误，并在调用任何业务 handler 前返回。

- [ ] **步骤 3：更新全部工具 schema**

增加统一 schema option：

```go
func intentOption() mcp.ToolOption {
	return mcp.WithString("intent", mcp.Required(), mcp.Description("本次模型请求的目标和预期结果"))
}
```

所有直接注册工具追加 `intentOption()`；`actionToolWithAnnotation` 的顶层 `required` 改为包含 `intent`；`changeExecuteInputSchema` 的顶层 properties/required 增加 intent。`workspace_list` 也必须要求 intent。

- [ ] **步骤 4：更新模型指导和 schema revision 测试**

在 `agent_guidance` 的请求契约中声明：每次工具调用都必须发送非空 intent；响应中展示 intent、准备动作、实际结果和验证证据。更新 schema revision 断言，确保 intent 契约改变会触发客户端刷新。

- [ ] **步骤 5：运行协议和 envelope 测试**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./internal/envelope ./internal/server -run 'Intent|Catalog|Guidance|Protocol' -count=1
```

预期：PASS；缺少 intent 的请求不会进入目标 handler，现有带有测试 intent 的合法请求保持原行为。

## 任务 4：接入 Runtime、工具调用和 Remote Session 事件

**文件：**

- 修改：`internal/server/runtime.go`、`internal/server/observability.go`
- 修改：`internal/remotesession/service.go` 及相关测试
- 创建：`internal/server/observation_bridge.go`
- 创建：`internal/server/observer_integration_test.go`

- [ ] **步骤 1：编写 Runtime 观察集成测试**

测试创建临时 `MCPX_HOME` 和 workspace，构造 Runtime，执行一个带 intent 的工具 handler，然后从观察 Store 断言：

- `tool.started` 在 `tool.completed` 之前。
- started 事件包含 intent 和脱敏输入。
- completed 事件包含实际状态、耗时和脱敏输出。
- handler 返回错误时事件状态为 error，不能显示为成功。
- 多个 Remote Session 的事件按 workspace 聚合。

- [ ] **步骤 2：增加 Runtime 观察服务和桥接器**

在 Runtime 增加观察服务字段，并在 `New` 中用现有 `state.Store.DB()` 创建 Store、Broker 和 Socket Server。桥接器提供：

```go
type observationBridge struct {
	store *observation.Store
	broker *observation.Broker
}

func (b *observationBridge) Record(ctx context.Context, event observation.Event) error
func (b *observationBridge) RecordToolStarted(ctx context.Context, name string, req envelope.Request, args map[string]any) error
func (b *observationBridge) RecordToolCompleted(ctx context.Context, name string, req envelope.Request, result *mcp.CallToolResult, callErr error, timing interactionTiming) error
```

`Record` 必须先 `Store.Append`，成功后再 `Broker.Publish`；记录失败通过现有 logging 输出，不改变已完成工具的业务响应。

- [ ] **步骤 3：在 instrumentTool 接入开始/结束事件**

在调用 handler 前解析请求并记录 `tool.started`；handler 返回后使用未重新包装的结果记录 `tool.completed`，随后继续现有 ARC 包装和 `logToolCall`。输入从 `req.GetArguments()` 获取，输出通过 `NormalizeToolOutput` 获取。workspace 优先使用请求字段；仅有 Remote Session ID 时通过现有 Remote Session 查询解析 workspace。

- [ ] **步骤 4：让 Remote Session 业务事件进入观察流**

为 `remotesession.Service` 增加可选回调：

```go
type EventObserver func(Session, Event)

func (s *Service) SetEventObserver(observer EventObserver)
```

`AddEvent` 成功插入后填充 sequence，再调用回调。Runtime 将 `remote_session.created`、`remote_session.updated`、`remote_session.attached`、`remote_session.handoff`、`changeset.*`、`task.stopped` 等事件映射为观察事件；回调错误不回滚业务事件。

- [ ] **步骤 5：增加 Changeset 文件变更事件**

在 `toolChangeExecute` 的实际 Apply 成功路径和 `applyChangeset` 的成功路径调用统一函数：

```go
func (r *Runtime) observeAppliedChangeset(ctx context.Context, req envelope.Request, session remotesession.Session, item changeset.Changeset) {
	dto := changeSummaryDTO(item)
	output, _ := json.Marshal(dto)
	bounded, _ := observation.SanitizeJSON(output, observation.MaxEventBytes)
	_ = r.observation.Record(ctx, observation.Event{
		Workspace:       session.WorkspaceName,
		RemoteSessionID: session.ID,
		RequestID:       req.RequestID,
		OperationID:     item.ID,
		Type:            "file.changed",
		Intent:          req.Intent,
		Output:          bounded,
		Summary:         item.Summary,
		ResourceURI:     fmt.Sprintf("mcpx://remote-sessions/%s/changesets/%s/diff", session.ID, item.ID),
	})
}
```

使用现有 `changeSummaryDTO` 的逐文件 Diff 预览和 resource URI，事件只保存脱敏、限长内容；预览/审批阶段只保留 `tool.completed`，实际 Apply 成功后才生成 `file.changed`。

- [ ] **步骤 6：启动和关闭 Socket**

在 `Runtime.Start` 的 HTTP 服务启动前启动 observation Socket；在 `Runtime.Close` 中先关闭 Socket/Broker，再关闭 Task Manager 和 SQLite。Socket 启动失败必须使服务启动失败并输出实际错误。

- [ ] **步骤 7：运行 Server 集成测试**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./internal/server ./internal/remotesession -run 'Observation|Event|Changeset' -count=1
```

预期：PASS；业务工具结果、既有 Remote Session 事件和观察事件同时可用，观察记录失败不会改变工具返回状态。

## 任务 5：接入 Terminal Task stdout/stderr 增量

**文件：**

- 修改：`internal/terminal/task.go`
- 修改：`internal/server/runtime.go`、`internal/server/observation_bridge.go`
- 创建或修改：`internal/terminal/task_observation_test.go`

- [ ] **步骤 1：编写输出回调失败测试**

创建 Task Manager，设置输出回调，运行会产生 stdout 和 stderr 的短命令，断言每个回调包含 task ID、Remote Session ID、workspace、流名称和递增 offset。

- [ ] **步骤 2：增加 Terminal 输出事件类型和 setter**

在 terminal 包中定义与 observation 包无关的回调接口，避免包循环依赖：

```go
type OutputChunk struct {
	TaskID          string
	RemoteSessionID string
	WorkspaceName   string
	Command         string
	Stream          string
	Offset          int64
	Data            []byte
}

type OutputSink func(OutputChunk)

func (m *TaskManager) SetOutputSink(sink OutputSink)
```

`lockedWriter.Write` 在现有日志写入完成后复制本次 chunk、释放 Task mutex，再调用 sink；sink 不得在锁内执行，避免 DB 写入阻塞 Task 状态和 stdin。

- [ ] **步骤 3：在 Runtime 绑定 sink 并记录 command.output**

Runtime 初始化 Task Manager 后设置 sink，将 `OutputChunk` 转换为观察事件。调用 `SanitizeText` 和输出大小限制，事件填充 `stream`、`stream_offset`、task operation ID；记录失败不返回给子进程 writer。

- [ ] **步骤 4：验证流式输出和终态输出**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./internal/terminal ./internal/server -run 'Output|Command|Task' -count=1
```

预期：PASS；命令运行期间收到 stdout/stderr 事件，命令结束后 `tool.completed` 仍包含实际退出码和最终受限输出。

## 任务 6：实现 `workspace` CLI 和终端渲染

**文件：**

- 创建：`cmd/mcpx-server/workspace.go`、`cmd/mcpx-server/workspace_test.go`
- 修改：`cmd/mcpx-server/main.go`
- 修改：`internal/observation/client.go`、`internal/observation/render.go`
- 修改：`README.md`

- [ ] **步骤 1：编写 CLI 参数和渲染失败测试**

测试覆盖：

- 缺少 workspace 参数返回非零退出码和 usage。
- `--history` 解析为正整数并限制上限。
- text renderer 输出 `INTENT`、`TOOL`、`RESULT`、`COMMAND OUTPUT`、`FILE CHANGES`。
- Diff 输出保留 `+`/`-` 行和截断提示。
- JSON renderer 每行输出一个事件 JSON。

- [ ] **步骤 2：实现 workspace 子命令入口**

在 `main` 的 subcommand 分支增加：

```go
case "workspace":
	os.Exit(runWorkspaceObserver(os.Args[2:]))
```

`runWorkspaceObserver` 使用独立 `flag.FlagSet` 解析 workspace、history、format；调用 `config.HomeDir()` 和 `observation.SocketPath(home)`，不调用 `server.New` 或 `Runtime.Start`，因此不会启动第二个 MCP HTTP 服务。

- [ ] **步骤 3：实现信号和只读客户端运行循环**

使用 `signal.NotifyContext` 监听 `SIGINT`/`SIGTERM`；正常 Ctrl-C 返回 0。Socket 不可用、workspace 不存在、协议错误返回非零码并输出实际错误，不执行任何工具操作。

- [ ] **步骤 4：实现 text/JSON renderer**

text renderer 按事件类型输出：

```go
func RenderText(w io.Writer, event Event, color bool) error
func RenderJSON(w io.Writer, event Event) error
```

工具开始显示 intent 和输入；工具完成显示状态、耗时、输出或错误；命令输出区分 stdout/stderr；文件事件显示逐文件操作、增删统计、bounded unified diff 和资源 URI；`truncated`、`gap`、`observer.notice` 必须明确显示。

- [ ] **步骤 5：更新帮助、README 和示例**

在 `printUsage` 增加命令说明，在 README 增加：

- MCP 调用必须携带 intent 的示例。
- `mcp-server workspace demo` 的启动和停止方式。
- text 输出时间线示例。
- 事件推送、历史回放、断线恢复、只读限制和脱敏限制。

- [ ] **步骤 6：运行 CLI 测试**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./cmd/mcpx-server ./internal/observation -run 'Workspace|Render|Client' -count=1
```

预期：PASS；帮助信息包含 `workspace`，渲染内容与观察协议事件一致。

## 任务 7：端到端验证和收尾检查

**文件：**

- 修改：`internal/server/observer_integration_test.go`、相关测试夹具和 `README.md`（仅在测试发现文档不一致时）

- [ ] **步骤 1：增加端到端观察流程测试**

使用临时 `MCPX_HOME`：

1. 启动 Runtime 的 observation Socket。
2. 连接 Client，订阅 workspace 并请求历史。
3. 执行带 intent 的普通工具。
4. 执行会产生 stdout/stderr 的命令。
5. 应用包含两个文件的 Changeset。
6. 断开 Client，产生新事件后重新连接并使用最后 sequence 恢复。
7. 断言事件顺序、workspace 聚合、脱敏、Diff 摘要、退出码和重连补偿。

- [ ] **步骤 2：运行格式和静态检查**

运行：

```bash
test -z "$(gofmt -l ./cmd ./internal)"
GOCACHE=/tmp/mcpx-go-cache go vet ./...
```

预期：第一条无输出且退出码为 0；`go vet` 退出码为 0。

- [ ] **步骤 3：运行完整测试和竞态检测**

运行：

```bash
GOCACHE=/tmp/mcpx-go-cache go test ./... -count=1
GOCACHE=/tmp/mcpx-go-cache go test -race ./... -count=1
```

预期：两条命令均 PASS；没有观察 Broker、Socket、Task sink 或 SQLite migration 的竞态报告。

- [ ] **步骤 4：运行构建验证**

运行：

```bash
CGO_ENABLED=0 go build -o bin/mcpx-server ./cmd/mcpx-server
```

预期：退出码为 0，生成本地 gitignore 的 `bin/mcpx-server`，不产生 workspace 内 diff 文件。

- [ ] **步骤 5：执行最终变更审查**

运行：

```bash
rtk git status -sb
rtk git diff --check
rtk git diff --stat
```

检查项：

- 只有计划范围内的 Go、测试、README 和规格/计划文件发生变化。
- 观测命令不会调用工具、审批或写文件逻辑。
- 所有用户可见的成功、失败、截断和未验证状态都有事件证据。
- 没有把原始密钥、token、password 或私钥写入观察事件。
- 没有生成 workspace 内的 diff 临时文件。

## 提交节奏

每完成一个逻辑任务，先运行该任务列出的测试并向用户报告真实结果；本仓库禁止代理自动执行 `git commit`、`git push` 或合并。需要提交时先展示文件变更摘要和完整中文 Conventional Commit message，等待用户明确确认后再操作。

## 规格覆盖自检

- CLI、只读生命周期、历史回放和实时展示：任务 2、任务 6、任务 7。
- 顶层 intent、严格拒绝和模型指导：任务 3。
- 工具输入输出、状态、耗时和错误：任务 1、任务 4。
- 命令 stdout/stderr 增量和终态：任务 5。
- 文件变更摘要和 unified Diff：任务 4、任务 6。
- workspace 聚合和 Remote Session 生命周期：任务 4。
- Unix Socket 事件推送、断线恢复和 gap 补偿：任务 2、任务 6、任务 7。
- 脱敏、大小上限、二进制处理和安全边界：任务 1、任务 2、任务 6。
- 失败、限制、未验证事项和真实验证结果：任务 4、任务 6、任务 7。
