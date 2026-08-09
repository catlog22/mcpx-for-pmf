# 对外 Tool 与观测性重设计实现计划

> **面向 AI 代理的工作者：** 本计划由当前会话内联执行；不执行 commit。步骤使用复选框跟踪进度，完成每个批次后运行对应测试和格式检查。

**目标：** 将 MCPX 当前 18 个公共 Tool 重构为 28 个语义清晰的 Tool，并补齐统一请求/响应、语义确认、自动执行追踪、终端呈现和 Workspace 历史查询，同时保持现有功能、ACL、策略、Task、Changeset、Resource 和 Secret 边界。

**架构：** 公共 Tool 通过新的严格 Schema 和路由适配层进入现有领域 Handler；只读同资源查询使用封闭 `view`，同一状态机的有限转移使用封闭 `operation`/`transition`。Runtime 在 Tool、命令、Skill 和 MCP 边界自动生成父子观测事件，脱敏后持久化并由终端和历史 Tool共同消费。

**技术栈：** Go、标准库 `testing`、SQLite、现有 MCP-Go、ARC 结果包装器、现有 Observation Socket/Store、现有 Task/Changeset/Remote Session 服务。

---

## 实现边界与文件清单

### 创建文件

- `internal/server/tool_dispatch.go`：28 个公共 Tool 到现有领域 Handler 的严格路由适配。
- `internal/server/tools_workspace_history.go`：`workspace_history_read` Handler、过滤校验和 ACL 入口。
- `internal/observation/history.go`：历史查询类型、SQL 构造、分页和脱敏结果投影。
- `internal/server/public_catalog_test.go`：28 个公共 Tool、Schema 和路由分支契约。
- `internal/server/tools_workspace_history_test.go`：MCP 历史查询 Handler 验证。
- `internal/observation/history_test.go`：ID、时间、关键词、类型、状态和分页查询验证。
- `internal/observation/trace_test.go`：Tool/命令/Skill/MCP 父子事件验证。

### 修改文件

- `internal/server/tools_catalog.go`：注册 28 个新公共 Tool，移除旧 `*_manage` 的公共注册，保留内部 Handler。
- `internal/server/capabilities.go`：同步 28 个 Tool 的领域、角色、Feature 和 Schema Manifest。
- `internal/server/agent_guidance.go`：更新路由、响应契约、确认和观测指导。
- `internal/server/tools_manage.go`：将旧 action 分派保留为内部适配所需的最小函数，公共 Schema 不再暴露旧入口。
- `internal/envelope/envelope.go`：增加新的扁平请求字段、状态和结构化恢复信息。
- `internal/server/observability.go`：调整公共参数注入、风险 Tool 的 `purpose` 和自动追踪入口。
- `internal/server/runtime.go`：更新 capability、结果状态和公共恢复动作。
- `internal/server/confirmation.go`、`internal/approval/store.go`：实现仅语义确认的 `confirmation_token`。
- `internal/server/tools_change_execute.go`、`internal/server/tools_command_execute.go`：接入新确认和幂等契约。
- `internal/server/tools_ext.go`：Skill/MCP 子调用观测和新调用入口。
- `internal/observation/event.go`、`store.go`、`normalize.go`、`redact.go`、`memory.go`：扩展事件字段、持久化和历史投影。
- `internal/server/observation_bridge.go`：记录命令、Skill、MCP 和文件变更子事件。
- `internal/terminal/task.go`、`internal/terminal/task_observation_test.go`：传递命令父子追踪字段。
- `internal/observation/render.go`、`timeline.go`、`palette.go`：渲染计划、Tool、命令、Skill、MCP 和嵌套结果。
- `cmd/mcpx-server/workspace.go`：增加详细度选项并保持 text/json 同源。
- `internal/state/migrations.go`：为新增观测字段和查询索引增加迁移。
- 相关 `*_test.go`：更新当前公共 Tool 名称、Schema、ARC、观测和接受测试。
- `README.md`：在实现验证后更新公共 Tool、历史查询和终端观测说明。

## 任务 1：建立 28 个公共 Tool 和严格路由

**文件：**

- 创建：`internal/server/tool_dispatch.go`
- 创建：`internal/server/public_catalog_test.go`
- 修改：`internal/server/tools_catalog.go`
- 修改：`internal/server/capabilities.go`
- 修改：`internal/server/agent_guidance.go`
- 修改：`internal/server/tools_manage.go`
- 测试：`internal/server/catalog_regression_test.go`、`internal/server/intent_test.go`、`internal/server/acceptance_protocol_test.go`

- [ ] **步骤 1：先锁定 28 个 Tool 名称和路由断言。** 在 `public_catalog_test.go` 中使用实际 `registerTools` 生成 MCP Server，断言名称集合严格等于：

```go
expected := []string{
    "workspace_list", "workspace_observe", "workspace_history_read",
    "session_open", "session_read", "session_transition",
    "source_read", "change_prepare", "change_read", "change_apply", "change_revert",
    "command_run", "task_read", "task_control", "progress_report",
    "plan_create", "plan_read", "plan_transition", "runtime_read",
    "environment_read", "environment_snapshot_create", "extension_discover",
    "skill_call", "mcp_call", "artifact_read", "artifact_register",
    "screenshot_capture", "secret_provide",
}
```

- [ ] **步骤 2：运行失败测试确认旧目录仍存在。**

运行：`rtk go test ./internal/server -run 'TestPublicCatalog|TestCatalog' -count=1`

预期：FAIL，当前注册结果仍包含 `session_manage`、`change_manage` 等旧 Tool，且缺少新 Tool。

- [ ] **步骤 3：在 `tools_catalog.go` 注册新 Tool。** 为每个 Tool 使用专用描述和 Annotation；只读投影使用 `view` 枚举，状态机使用 `operation` 或 `transition` 枚举，所有分支设置独立必填字段。

每个公共 Schema 需要满足：

```go
properties["view"] = map[string]any{"type": "string", "enum": views}
properties["operation"] = map[string]any{"type": "string", "enum": operations}
```

注册代码不得把 `action` 放入公共顶层 Schema，也不得注册旧 `*_manage` 名称。

- [ ] **步骤 4：实现 `tool_dispatch.go` 的适配路由。** 路由只把新的封闭字段转换为现有内部 Handler 所需的 action；外部请求不会看到旧 action。各路由必须显式列举分支：

```go
func (r *Runtime) toolWorkspaceObserve(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    view := requiredEnum(req, "view")
    switch view {
    case "changes":
        return r.toolWorkspaceChanges(ctx, req)
    case "snapshot":
        return r.toolFileSnapshot(ctx, req)
    case "diff":
        return r.toolFileChanges(ctx, req)
    case "watch":
        return r.toolFileWatch(ctx, req)
    case "memory":
        return r.toolWorkspaceMemory(ctx, req)
    default:
        return r.invalidView(ctx, req, "workspace_observe", view)
    }
}
```

同样实现 `session_read`、`session_transition`、`source_read`、`change_read`、`task_read`、`task_control`、`plan_transition`、`runtime_read`、`environment_read`、`extension_discover` 和 `artifact_read`。

- [ ] **步骤 5：更新 capability manifest 和 Agent 指引。** `capabilityToolNames`、`machineToolCapabilities`、`runtimeToolCapabilities` 和推荐调用链全部使用新名称；Skill 和 MCP 调用分别指向 `skill_call`、`mcp_call`。

- [ ] **步骤 6：运行目录和 Schema 测试。**

运行：`rtk go test ./internal/server -run 'TestPublicCatalog|TestCapabilityCatalog|Test.*Schema|TestEveryRegisteredTool' -count=1`

预期：PASS；工具数量为 28，旧 `*_manage` 不在 `tools/list`，所有公共 Tool 仍有必要的安全 Annotation 和结构化 Schema。

## 任务 2：统一请求、响应、确认和恢复契约

**文件：**

- 修改：`internal/envelope/envelope.go`
- 修改：`internal/server/observability.go`
- 修改：`internal/server/runtime.go`
- 修改：`internal/server/confirmation.go`
- 修改：`internal/approval/store.go`
- 修改：`internal/server/tools_change_execute.go`
- 修改：`internal/server/tools_command_execute.go`
- 测试：`internal/envelope/envelope_test.go`、`internal/approval/store_test.go`、`internal/server/intent_test.go`、`internal/server/runtime_logic_test.go`

- [ ] **步骤 1：编写新请求字段测试。** 覆盖 `session_id`、`purpose`、`idempotency_key`、`confirmation_token` 的扁平解析和服务端生成 `request_id`。

```go
func TestParseRequestUsesFlatPublicFields(t *testing.T) {
    req, err := ParseRequest(json.RawMessage(`{"session_id":"s1","purpose":"verify","idempotency_key":"k1"}`))
    if err != nil {
        t.Fatal(err)
    }
    if req.SessionID != "s1" || req.Purpose != "verify" || req.IdempotencyKey != "k1" {
        t.Fatalf("request = %+v", req)
    }
    if req.RequestID == "" {
        t.Fatal("request id was not generated")
    }
}
```

- [ ] **步骤 2：为高风险 Tool 增加 `purpose` Schema 和必填检查。** `command_run`、变更、Task 控制、Skill/MCP 调用和长任务必须有 `purpose`；普通只读查询允许省略。公共 Schema 不再将 `intent` 作为通用必填字段。

- [ ] **步骤 3：定义统一状态和错误恢复结构。** 增加 `succeeded`、`accepted`、`waiting_confirmation`、`failed`、`interrupted` 状态，以及 `recovery.action` 固定枚举；`resultJSON` 和 ARC 分类器只使用一个状态来源。

```go
type Recovery struct {
    Action string         `json:"action"`
    Tool   string         `json:"tool,omitempty"`
    Details map[string]any `json:"details,omitempty"`
}
```

- [ ] **步骤 4：实现语义确认 Token。** `approval.Pending` 增加确认摘要、Principal、Session 和规范化操作摘要；`Store` 提供按 Token、Session、Principal 和摘要校验的读取方法。Token 只跳过再次确认，不替代权限、策略、版本和冲突检查。

- [ ] **步骤 5：替换 `user_confirmed` 分支。** `change_apply` 和 `command_run` 首次返回 `waiting_confirmation` 及 `confirmation_token`；相同原始参数重试时消费 Token。错误参数或摘要变化返回 `CONFIRMATION_MISMATCH`。

- [ ] **步骤 6：运行契约和回归测试。**

运行：`rtk go test ./internal/envelope ./internal/approval ./internal/server -run 'Test(ParseRequest|Confirmation|Approval|Runtime|Command|Change)' -count=1`

预期：PASS；Token 不能跨 Session/Principal/Tool 使用，相同幂等键不重复执行，结果未知时返回 `interrupted` 或要求查询状态。

## 任务 3：建立 Runtime 自动执行追踪

**文件：**

- 修改：`internal/observation/event.go`
- 修改：`internal/observation/store.go`
- 修改：`internal/observation/normalize.go`
- 修改：`internal/observation/redact.go`
- 修改：`internal/server/observability.go`
- 修改：`internal/server/observation_bridge.go`
- 修改：`internal/server/tools_command_execute.go`
- 修改：`internal/server/tools_ext.go`
- 修改：`internal/terminal/task.go`
- 修改：`internal/state/migrations.go`
- 创建：`internal/observation/trace_test.go`
- 测试：`internal/server/observer_integration_test.go`、`internal/server/observability_regression_test.go`、`internal/terminal/task_observation_test.go`

- [ ] **步骤 1：添加观测字段和事件常量。** `Event` 增加 `EventID`、`Kind`、`Name`、`Status`、`ParentOperationID`、`Purpose`、`CWD`、`ExitCode`、`DurationMs` 和 `Provider`；保留现有字段以支持历史读取和文件变化摘要。

```go
const (
    TypeAgentUpdate      = "agent.update"
    TypeExecutionTrace   = "execution.trace"
    TypeCommandStarted   = "command.started"
    TypeCommandCompleted = "command.completed"
    TypeSkillStarted     = "skill.started"
    TypeSkillCompleted   = "skill.completed"
    TypeMCPStarted       = "mcp.started"
    TypeMCPCompleted     = "mcp.completed"
)
```

- [ ] **步骤 2：添加 SQLite 迁移和索引。** 在 `internal/state/migrations.go` 增加事件字段迁移，并为 `(workspace_name, created_at)`、`request_id`、`operation_id`、`event_id` 建立索引；迁移必须可重复执行。

- [ ] **步骤 3：让 `instrumentTool` 自动记录 Tool 事件。** started/completed 事件记录公共 Tool、Purpose、状态、耗时和脱敏输入；错误结果记录结构化 code/category，不从模型文本推断成功。

- [ ] **步骤 4：接入命令子事件。** `command_run` 启动 Task 时传递 `request_id`、`operation_id`、`parent_operation_id`、实际命令和工作目录；Task 完成时记录退出码、耗时和结果未知状态。

- [ ] **步骤 5：接入 Skill/MCP 子事件。** `skill_call` 包住 `skill.Execute`，`mcp_call` 包住上游 Proxy 调用；事件中分别记录 Skill 名称或 MCP Server/Tool 名称，调用参数先经过现有 Observation 脱敏器。

- [ ] **步骤 6：覆盖父子调用测试。** 构造一个 `mcp_call → skill_call → command` 和一个直接 `command_run` 场景，断言每个事件有独立名称、状态和 `parent_operation_id`。

运行：`rtk go test ./internal/observation ./internal/server ./internal/terminal -run 'Test(Trace|Observation|TaskObservation|Instrument)' -count=1`

预期：PASS；没有 completed 的 started 事件被标记为中断/未知，Secret 和敏感参数不出现在 Input、Output 或 Summary。

## 任务 4：实现 Workspace 历史查询

**文件：**

- 创建：`internal/observation/history.go`
- 创建：`internal/observation/history_test.go`
- 创建：`internal/server/tools_workspace_history.go`
- 创建：`internal/server/tools_workspace_history_test.go`
- 修改：`internal/observation/store.go`
- 修改：`internal/server/tools_catalog.go`
- 修改：`internal/server/capabilities.go`
- 修改：`internal/server/agent_guidance.go`
- 测试：`internal/server/observer_integration_test.go`

- [ ] **步骤 1：先编写查询类型和失败测试。** 定义精确字段，禁止模糊的 `id`：

```go
type HistoryQuery struct {
    Workspace     string
    SessionID     string
    EventIDs      []string
    RequestIDs    []string
    OperationIDs  []string
    TaskIDs       []string
    ChangesetIDs  []string
    CreatedAfter  *time.Time
    CreatedBefore *time.Time
    Keyword       string
    Kinds         []string
    Statuses      []string
    Limit         int
    Cursor        string
}
```

- [ ] **步骤 2：实现安全 SQL 构造。** 过滤字段之间使用 AND，数组内使用 OR；关键词只匹配脱敏的摘要、Purpose、Tool、Name、Provider、路径和错误码；所有值使用参数绑定，不能拼接用户字符串。

- [ ] **步骤 3：实现时间线分页。** 默认最新倒序；Cursor 编码最后一个 `sequence` 和时间边界；`after_sequence` 仍由观测 Socket 使用，不与公开历史查询的 Cursor 混淆。

- [ ] **步骤 4：实现 `workspace_history_read` Handler。** 通过当前 Principal 和 Session ACL 校验 Workspace 范围，返回 `event_id`、`sequence`、时间、kind、name、status、summary、引用 ID 和 Resource URI，不返回未经脱敏的完整 Input/Output。

- [ ] **步骤 5：添加 Handler 测试。** 覆盖 event/request/operation/task/changeset ID、时间区间、关键词、类型、状态、组合 AND/OR、分页、跨 Session ACL 和结果脱敏。

运行：`rtk go test ./internal/observation ./internal/server -run 'Test(History|WorkspaceHistory|Memory)' -count=1`

预期：PASS；同一 Workspace 可检索多个 Session，跨 Workspace 或无 ACL 返回拒绝，关键词不匹配 Secret 和原始敏感输出。

## 任务 5：升级终端呈现

**文件：**

- 修改：`internal/observation/render.go`
- 修改：`internal/observation/timeline.go`
- 修改：`internal/observation/palette.go`
- 修改：`cmd/mcpx-server/workspace.go`
- 测试：`internal/observation/render_test.go`、`internal/observation/timeline_test.go`、`cmd/mcpx-server/workspace_test.go`

- [ ] **步骤 1：先添加渲染失败断言。** 对 `agent.update`、Tool、命令、Skill、MCP 事件分别断言稳定前缀和名称；断言嵌套子事件按父操作归并。

```go
want := []string{
    "[plan]",
    "[TOOL] source_read",
    "[CMD] go test ./...",
    "[SKILL] code-review",
    "[MCP] github · search_code",
}
```

- [ ] **步骤 2：扩展交互块状态。** `interactionBlock` 保存 kind、name、parent、purpose、status 和是否已输出结果；开始事件显示身份，完成事件显示事实，started 无 completed 显示中断/未知。

- [ ] **步骤 3：实现 text 模式渲染。** 默认输出完整调用身份和关键结果；verbose 模式增加脱敏命令参数、CWD、Task ID、退出码和耗时；保留当前宽度、颜色、Diff 和截断预算。

- [ ] **步骤 4：实现 JSON 同源输出。** `RenderJSON` 直接输出完整事件；text 和 JSON 必须来自同一个事件，不允许 text 依赖模型自行拼出的摘要。

- [ ] **步骤 5：增加详细度参数并验证 CLI。** 在 `workspace` 子命令加入 `-detail compact|verbose`，默认 `compact`；`-format json` 忽略终端详细度渲染但保留完整字段。

- [ ] **步骤 6：运行终端测试。**

运行：`rtk go test ./internal/observation ./cmd/mcpx-server -run 'Test(Render|Timeline|WorkspaceObserver)' -count=1`

预期：PASS；终端可见 Tool、命令、Skill、MCP，计划和执行事实不混淆，重复进度不会刷屏，断线重放仍能恢复调用块。

## 任务 6：更新能力文档并完成集成验证

**文件：**

- 修改：`README.md`
- 修改：`internal/server/acceptance_protocol_test.go`
- 修改：`internal/server/reconnect_acceptance_test.go`
- 修改：`internal/server/agent_guidance_test.go`
- 修改：`internal/server/presentation_regression_test.go`
- 修改：`internal/arc/arc.go`、`internal/arc/arc_test.go`

- [ ] **步骤 1：更新接受测试的 Tool 清单。** 断言 28 个 Tool、3 个 Resource、无旧 `*_manage` 公共入口，并验证各 `view`/`transition` Schema 的必填字段。

- [ ] **步骤 2：更新 ARC 显示分类。** 将新 Tool 名称映射到现有 Markdown、Diff、Plan、Task、Error 等展示类型，确认 `waiting_confirmation`、`interrupted` 和 `failed` 不被误判为成功。

- [ ] **步骤 3：更新 README。** 用新目录替换旧 18 Tool 表格，补充 `workspace_history_read` 过滤示例、自动执行追踪示例和 28 Tool/3 Resource 边界；保留当前能力覆盖说明。

- [ ] **步骤 4：运行改动包测试。**

运行：`rtk go test ./internal/envelope ./internal/approval ./internal/observation ./internal/server ./internal/terminal ./internal/arc ./cmd/mcpx-server -count=1`

预期：PASS。

- [ ] **步骤 5：运行格式和静态检查。**

运行：

```bash
rtk gofmt -w cmd/mcpx-server internal/approval internal/arc internal/envelope internal/observation internal/server internal/state internal/terminal
rtk sh -c 'test -z "$(gofmt -l ./cmd ./internal)"'
rtk go vet ./...
```

预期：格式检查无输出，`go vet` 成功。

- [ ] **步骤 6：运行完整测试和构建。**

运行：

```bash
rtk go test ./... -count=1
rtk go test -race ./... -count=1
rtk proxy env CGO_ENABLED=0 go build -o bin/mcpx-server ./cmd/mcpx-server
```

预期：测试、竞态检测和 CGO 关闭构建全部成功；本次不执行 commit。

## 计划自检

- 28 个公共 Tool 均有任务 1 的注册、Schema 和测试覆盖。
- 现有 18 个 Tool 的全部 action 分支均在任务 1 的适配或任务 6 的覆盖测试中有对应入口。
- 3 个 MCP Resource 在任务 6 中有接受测试和文档校验。
- 请求/响应、确认、幂等、错误恢复、自动追踪、历史过滤、终端 text/json 均有独立任务和测试命令。
- 所有新增文件和修改文件已列出，未引用未定义的函数或类型。
- 计划不包含 commit 步骤，符合用户本轮“不 commit”要求。
