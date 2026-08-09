# 对外操作与终端观测性设计

- 日期：2026-08-04
- 状态：待用户审查
- 范围：重新设计 MCPX 对外 MCP Tool、Resource、统一请求/响应契约与终端观测性
- 兼容性：不保留现有公共 Tool 名称和参数结构的兼容要求

## 1. 目标与非目标

### 目标

1. 让每个对外操作都能用一句稳定语义描述：对某类资源执行一个明确动作，并产生可验证结果。
2. 在不丢失现有能力的前提下，把公共 Tool 收敛到 20～30 个；目标目录为 28 个 Tool。
3. 让终端同时展示模型公开表达的目标/计划，以及 Runtime 实际执行的 Tool、命令、Skill 和上游 MCP。
4. 提供可按事件 ID、请求 ID、操作 ID、时间、关键词、类型和状态查询的 Workspace 活动历史。
5. 统一确认、幂等、版本冲突、异步 Task、中断和错误恢复语义。

### 非目标

- 不暴露模型隐藏思维链、逐 token 推理、内部概率或未公开的分析过程。
- 不把 Workspace 活动历史与 Git 提交历史混为一体。
- 不通过兼容层继续保留旧的 `*_manage(action=...)` 公共入口。
- 不让模型自报执行事实；执行事实必须由 Runtime 自动产生。

## 2. 设计原则

### 2.1 工具边界

- 禁止跨领域的 `*_manage` 工具。
- 一个 Tool 只有一个主语义。
- 不同副作用、权限、确认模型、重试语义或结果结构的操作必须拆开。
- 同一资源的纯只读查询可以使用封闭的 `view` 投影。
- 同一资源、同一状态机内的有限状态转移可以使用封闭的 `operation`/`transition` 分支；不得借此重新创建通用管理器。
- 公共 Schema 不暴露 `payload`、自由字符串 `action` 或通用 `target`。
- 批量只用于天然原子的操作，例如一个 `change_apply` 包含多个文件变更。

### 2.2 计划与事实

- `progress_report` 记录模型公开的目标、阶段、当前动作、下一步和证据。
- `tool.started`、`command.started`、`skill.started`、`mcp.started` 等事件由 Runtime 自动记录。
- 计划中的 `next` 不代表动作已发生；只有对应完成事件才能形成执行事实。

## 3. 公共 Tool 目录

以下为目标公共 Tool 目录，共 28 个。表中的 `view`、`operation` 和 `transition` 都是封闭枚举，并使用与分支匹配的严格 Schema。

| 领域 | Tool | 语义与允许分支 |
| --- | --- | --- |
| Workspace | `workspace_list` | 列出可用 Workspace |
| Workspace | `workspace_observe` | `changes`、`snapshot`、`diff`、`watch`、`memory` |
| Workspace | `workspace_history_read` | 查询 Workspace 持久化活动历史 |
| Session | `session_open` | 创建或恢复 Session |
| Session | `session_read` | `list`、`summary`、`events` |
| Session | `session_transition` | `update`、`handoff`、`attach`、`close` |
| Source | `source_read` | `file`、`search`、`list`、`context` |
| Change | `change_prepare` | 创建 Changeset 草稿 |
| Change | `change_read` | `diff`、`history` |
| Change | `change_apply` | 应用变更 |
| Change | `change_revert` | 回滚已应用变更 |
| Command | `command_run` | 执行命令或项目任务 |
| Task | `task_read` | `list`、`status`、`logs`、`ports`、`diagnostics` |
| Task | `task_control` | `attach`、`stop`、`stdin` |
| Observability | `progress_report` | 提交模型公开的进度状态 |
| Plan | `plan_create` | 创建持久化 Plan |
| Plan | `plan_read` | 读取 Plan |
| Plan | `plan_transition` | `start_task`、`complete_task`、`block_task`、`replan`、`deliver` |
| Runtime | `runtime_read` | `capabilities`、`project`、`instructions` |
| Environment | `environment_read` | `current`、`compare` |
| Environment | `environment_snapshot_create` | 创建环境快照 |
| Extension | `extension_discover` | `kind=skill/mcp`，`view=list/describe` |
| Skill | `skill_call` | 调用 Skill |
| MCP | `mcp_call` | 调用上游 MCP 工具 |
| Artifact | `artifact_read` | `list`、`content` |
| Artifact | `artifact_register` | 注册 Artifact |
| Special | `screenshot_capture` | 截取屏幕 |
| Special | `secret_provide` | 提供仅驻留进程内的 Secret |

### 3.1 现有能力覆盖

| 现有公共能力 | 新公共入口 |
| --- | --- |
| `workspace_list` | `workspace_list` |
| `session_open` | `session_open` |
| `file_read` | `source_read(view=file)` |
| `context_query(query/search/list)` | `source_read(view=context/search/list)` |
| `change_execute` | `change_apply`、`change_revert` |
| `change_manage(prepare/diff/history)` | `change_prepare`、`change_read(view=diff/history)` |
| `command_execute` | `command_run` |
| `task_manage` | `task_read`、`task_control` |
| `progress_report` | `progress_report` |
| `session_manage` | `session_read`、`session_transition` |
| `plan_manage` | `plan_create`、`plan_read`、`plan_transition` |
| `runtime_inspect` | `runtime_read` |
| `environment_inspect` | `environment_read`、`environment_snapshot_create` |
| `workspace_state` | `workspace_observe` |
| `extension_manage` | `extension_discover`、`skill_call`、`mcp_call` |
| `artifact_manage` | `artifact_read`、`artifact_register` |
| `screenshot_capture` | `screenshot_capture` |
| `secrets_provide` | `secret_provide` |

现有能力中的 Session、Task、Plan、Changeset、Environment Snapshot、Workspace Memory、Artifact、MCP Resource、ACL、确认和恢复流程均必须保留，只改变公共入口和结构。

## 4. 请求契约

公共参数保持扁平，避免再引入通用容器。

```json
{
  "session_id": "session_123",
  "purpose": "验证 refresh token 轮换逻辑",
  "idempotency_key": "client-operation-123",
  "path": "internal/oauth/token.go"
}
```

规则：

- `session_id` 只出现在 Session 作用域操作；Workspace 从 Session 解析。
- `purpose` 是面向用户和审计的简短说明，不参与路由、授权或业务判断。读取类操作可选；变更、执行、扩展调用和长任务操作必填。
- `request_id`、`operation_id` 由服务端生成，客户端不能伪造。
- `idempotency_key` 只用于可能产生副作用的操作。
- `confirmation_token` 只用于语义确认，不是身份认证、授权凭据或可转移 Bearer Token。
- 不使用 `payload`、自由字符串 `action` 或通用 `target`。

## 5. 响应契约

响应只有一个状态来源：

```json
{
  "status": "succeeded",
  "data": {},
  "meta": {
    "request_id": "req_123",
    "operation_id": "op_123",
    "session_id": "session_123"
  }
}
```

公开状态固定为：

```text
succeeded
accepted
waiting_confirmation
failed
interrupted
```

失败响应：

```json
{
  "status": "failed",
  "error": {
    "code": "REVISION_CONFLICT",
    "category": "conflict",
    "retryable": true,
    "message": "目标文件已发生变化",
    "details": {},
    "recovery": {
      "action": "read_current_state",
      "tool": "source_read"
    }
  }
}
```

机器客户端只依赖 `status`、`error.code`、`error.category`、`retryable` 和 `recovery.action`；`message` 用于人类阅读。

## 6. 确认、幂等与状态机

需要确认时返回：

```json
{
  "status": "waiting_confirmation",
  "error": {
    "code": "CONFIRMATION_REQUIRED",
    "category": "confirmation",
    "retryable": true,
    "details": {
      "confirmation_token": "opaque-reference",
    }
  }
}
```

客户端使用原 Tool 和原参数，仅增加 `confirmation_token` 重试。服务端仍需重新执行权限、策略、版本和冲突检查。

确认凭据必须绑定：

- 原始规范化参数和操作摘要；
- 当前 Session 与 Principal；
- 当前版本和安全策略上下文。

参数、Session、Principal 或摘要变化后确认失效。Token 不能用于其他 Tool，也不能绕过认证、授权、文件策略或命令策略。

操作状态：

```text
requested → waiting_confirmation → executing → succeeded / failed
requested → accepted → running → succeeded / failed / interrupted
```

如果进程在完成事件前中断，结果为 `interrupted`，不能推断操作未执行；客户端必须先查询 Task、文件版本或 Changeset 再决定是否重试。

## 7. 错误与恢复

错误分类固定为：

```text
validation
permission
confirmation
not_found
conflict
runtime
internal
```

恢复动作固定为：

```text
retry_same
read_current_state
request_confirmation
inspect_task
reauthenticate
abort
```

恢复规则：

- 参数错误：修正后重试，不能原样重试。
- 确认错误：使用同一操作和确认凭据重试。
- 版本冲突：重新读取并生成新操作。
- 权限错误：补充权限或 Secret，不能依赖重试绕过。
- 运行时错误：判断操作是否已经启动。
- 中断或结果未知：先查询状态，再决定是否重试。
- 内部错误：默认不自动重试，保留 request ID 和诊断信息。

## 8. Workspace 活动历史

`workspace_history_read` 查询持久化活动历史，不等同于 Git 提交历史。

支持的过滤字段：

```json
{
  "session_id": "session_123",
  "event_ids": ["evt_100"],
  "request_ids": ["req_123"],
  "operation_ids": ["op_456"],
  "task_ids": ["task_789"],
  "changeset_ids": ["cs_001"],
  "created_after": "2026-08-04T00:00:00Z",
  "created_before": "2026-08-04T23:59:59Z",
  "keyword": "refresh token",
  "kinds": ["tool", "command", "skill", "mcp", "file_change"],
  "statuses": ["succeeded", "failed"],
  "limit": 50,
  "cursor": "..."
}
```

查询规则：

- 不同过滤字段使用 AND；同一数组内使用 OR。
- `keyword` 搜索摘要、目的、工具名、命令、Skill、MCP、路径和错误码。
- 不搜索或返回未经脱敏的 Secret、Token、密码和完整敏感内容。
- 默认按最新事件倒序返回。
- `sequence` 用于时间线续读；`event_id` 用于精确定位。
- 跨 Session 查询受 Principal ACL 限制。
- 大型命令输出、完整 Diff 和 Artifact 通过 Resource URI 读取。

历史事件至少覆盖：

```text
agent.update
tool
command
skill
mcp
file_change
task
session
confirmation
error
```

## 9. 终端观测性

### 9.1 模型公开状态

`progress_report` 的状态字段：

```text
planning
executing
verifying
waiting
blocked
completed
```

内容结构：

```json
{
  "phase": "verifying",
  "current": "已完成 OAuth 令牌持久化修改，正在运行相关测试",
  "next": "检查旧 refresh token 是否失效",
  "evidence": [
    "已修改 2 个文件",
    "尚未获得测试结果"
  ],
  "status": "in_progress"
}
```

这表示模型公开的工作状态，不表示隐藏思维链。

### 9.2 Runtime 执行事实

Runtime 自动生成以下事件：

```text
agent.update
tool.started / tool.completed
command.started / command.completed
skill.started / skill.completed
mcp.started / mcp.completed
command.output
file.changed
session.lifecycle
observer.notice
```

每个事件至少包含：

```text
sequence
event_id
request_id
operation_id
parent_operation_id
kind
name
status
started_at
completed_at
summary
redacted_input
```

终端默认显示：

- 所有 MCPX Tool 名称；
- 实际执行命令、工作目录、退出码和耗时；
- Skill 名称；
- MCP Server 和上游工具名称；
- Skill/MCP 内部触发的嵌套命令；
- 成功、失败、中断和结果未知状态。

示例：

```text
[TOOL] extension call
  ↳ [SKILL] code-review
      ↳ [CMD] go test ./...
      ↳ [MCP] github · search_code
  ↳ succeeded
```

text 模式显示压缩后的完整调用身份；verbose 模式增加脱敏参数、Task ID、工作目录、耗时和退出码；JSON 模式输出完整结构化事件。

执行事件是 Runtime 事实，不能依赖模型在 `progress_report` 中复述，也不能把模型计划当成执行结果。

### 9.3 数据流

```text
MCP 请求
  → Gateway 分配 request_id
  → tool.started
  → 公共 Tool 执行
      → command / skill / mcp 子事件
      → file.changed / task.output 等结果事件
  → tool.completed
  → 脱敏持久化
  → Socket 推送
  → 终端按 parent_operation_id 归并
```

断线后按 `sequence` 从持久化 Store 补齐；started 没有 completed 时显示 `interrupted/unknown`。

## 10. MCP Resource

保留以下只读 Resource：

- `mcpx://remote-sessions/{session_id}/artifacts/{artifact_id}`：Artifact 内容
- `mcpx://remote-sessions/{session_id}/changesets/{changeset_id}/diff`：完整 Changeset Diff
- `mcpx://remote-sessions/{session_id}/tasks/{task_id}/logs`：完整 Task 日志

Resource 不计入 28 个 Tool，但必须遵守同样的 ACL、脱敏、大小限制和 Session 归属校验。

## 11. 安全与数据边界

- `confirmation_token` 不是认证或授权凭据。
- Secret 只在进程内短期存在，不写入 SQLite、日志、观测事件或终端。
- 命令、Skill、MCP 参数在事件和终端中脱敏。
- 公共历史只返回当前 Principal 可见的 Session 和 Workspace 活动。
- 完整日志、Diff、Artifact 使用 Resource URI 和独立大小限制。
- OAuth、Bearer、ACL、命令策略和文件策略仍然在 Tool handler 执行前后生效。

## 12. 验证标准

### Schema

- 公共 Tool 不再使用 `*_manage`。
- 副作用操作不使用泛化 `action`。
- 只读 `view`、状态转移 `operation` 和 `transition` 均为封闭枚举。
- 不暴露 `payload` 或通用 `target`。
- 每个 Tool 明确只读、幂等、破坏性和开放世界 Annotation。
- Tool Schema、描述、Annotation 和 capability manifest 来自同一注册源。

### 协议与状态

- 确认凭据只能确认原始操作。
- 相同幂等键不重复执行副作用操作。
- 版本冲突要求重新读取。
- 中断时不能声称操作未执行。
- 所有恢复错误具有稳定 code、category 和 recovery action。

### 观测与历史

- Tool、命令、Skill、MCP 均生成独立事件。
- 子事件可以通过 parent_operation_id 还原调用链。
- 命令记录实际命令、工作目录、退出码和耗时。
- 历史支持 ID、时间、关键词、类型、状态和分页游标。
- Secret、Token、密码和敏感参数始终脱敏。
- 断线后可以从持久化事件恢复。
- 只有 completed 事件才能产生成功事实。

### 终端验收

1. 模型调用 Tool 时显示 Tool 名称和 purpose。
2. 命令执行时显示脱敏命令、目录、退出码和耗时。
3. Skill/MCP 调用显示具体名称，而不是只显示扩展入口。
4. Skill/MCP 内部命令显示嵌套关系。
5. 终端区分模型计划、Runtime 执行和已验证结果。
6. 失败、等待确认、阻塞和结果未知都有明确状态。
7. text 与 JSON 模式表达相同事实。

## 13. 完整性约束

实现时必须以“现有能力覆盖表”和公共 Tool 目录作为逐项检查表。任何现有功能只能通过新的明确入口迁移，不能因删除 `*_manage` 或压缩 Tool 数量而丢失。

本规格完成后，先进行占位符、内部一致性、范围和歧义自检；通过用户书面审查后再创建实现计划。
