# Workspace 实时观测设计规格

状态：已批准

日期：2026-08-02

## 1. 背景

当前 MCPX 服务端可以记录 Remote Session、任务、Changeset 和部分操作事件，但缺少一个面向开发者的只读观测入口。用户只能看到会话或文件名称，无法连续看到模型的当前意图、工具调用参数、命令输出、文件变更内容和实际结果。

本功能增加命令：

```bash
mcp-server workspace <workspace name>
```

该命令用于在服务端本机打开一个 workspace 观测实例，以接近终端 Agent/Codex 的时间线展示模型活动。观测实例不能与模型交互，也不能执行工具、审批命令或修改工作区。

## 2. 目标与非目标

### 2.1 目标

- 按 workspace 聚合现有及后续创建的全部 Remote Session。
- 启动时回放最近历史事件，之后通过事件推送实时展示。
- 每次 MCP 工具调用都必须携带模型 intent，并在观测中展示。
- 展示工具输入、工具输出、命令 stdout/stderr、错误和耗时。
- 展示逐文件变更摘要、增删统计和受限的 unified diff。
- 观测输出默认脱敏并限制大小。
- 服务重启或连接中断后，可以使用序列号恢复观测进度。
- 观测过程不在 workspace 内创建 diff 临时文件或其他产物。

### 2.2 非目标

- 不提供观测端到模型或服务端的交互控制能力。
- 不替代现有 Remote Session、Task、Changeset 和审批接口。
- 不引入独立消息队列或外部事件服务。
- 不默认输出未经脱敏的原始输入、输出或任务日志。

## 3. CLI 行为

### 3.1 基本命令

```bash
mcp-server workspace <workspace name>
```

命令行为：

1. 校验 workspace 名称。
2. 创建一个只读的本地 observation instance，不创建可执行工具的 Remote Session。
3. 聚合该 workspace 下现有和后续创建的所有 Remote Session。
4. 回放最近历史事件。
5. 订阅并展示后续实时事件。
6. 只响应 Ctrl-C 退出，不读取 stdin。

观测进程自身不写入 workspace，只使用 `~/.mcpx/` 下的运行时状态、事件存储和本地 IPC Socket。

### 3.2 可选参数

```bash
mcp-server workspace <workspace name> --history 100 --format text
mcp-server workspace <workspace name> --format json
```

- `--history`：启动时回放的历史事件数量。
- `--format text`：默认的友好终端时间线。
- `--format json`：输出机器可读的事件流。
- TTY 环境默认启用颜色；输出重定向时使用纯文本。

## 4. intent 请求契约

### 4.1 请求格式

intent 作为同一次 MCP 工具调用的顶层字段传入：

```json
{
  "intent": "定位测试失败原因并修复相关代码",
  "remote_session_id": "rs_xxx",
  "workspace": "mcpx",
  "command": "go test ./..."
}
```

现有请求会先经过 `envelope.ParseRequest`，随后由 `remoteRequest` 统一处理。因此 `envelope.Request` 增加 `Intent` 字段，解析时保留在请求封装中，不将其误并入业务 `Payload`。

### 4.2 校验规则

- 所有公开 MCP 工具调用都必须携带非空 intent。
- intent 去除首尾空白后仍为空时拒绝请求。
- intent 超过约定长度上限时拒绝请求。
- 校验发生在工具业务处理前，不产生工具副作用。
- 缺失 intent 返回稳定的 `INTENT_REQUIRED` 错误码；超限返回参数校验错误。
- 所有工具的输入 schema 将 intent 标记为必填。
- 工具 schema revision 和 capability revision 随契约变更递增。
- `agent_guidance` 明确要求模型每次工具调用携带 intent。

`command_execute` 继续保留现有的 `purpose` 字段。intent 表示本次模型请求的整体目标，purpose 表示命令执行的具体用途，二者可以相同但语义不合并。

该契约是有意的兼容性变更。未升级的客户端会收到 `INTENT_REQUIRED`，不会执行工具。

## 5. 观察事件模型

新增独立的持久化观察事件流，避免改变现有 Remote Session 业务事件的语义和访问接口。

### 5.1 事件类型

- `tool.started`：工具名、intent、脱敏后的工具输入。
- `tool.completed`：实际状态、耗时、脱敏后的工具输出或错误。
- `command.output`：命令执行期间的 stdout/stderr 增量。
- `file.changed`：文件路径、操作类型、增删行数、变更摘要和有限 Diff。
- `session.lifecycle`：Remote Session 创建、连接、关闭等状态变化。
- `observer.notice`：重连、补偿、丢失区间和服务状态提示。

### 5.2 事件字段

每条事件包含：

- 全局递增 `sequence`。
- `workspace_name`。
- 可选的 `remote_session_id`。
- `request_id` 和 `operation_id`。
- 工具名、事件类型和状态。
- 脱敏后的 `intent`。
- 脱敏并限长后的输入、输出或日志片段。
- 事件摘要、资源 URI 和创建时间。
- `truncated`、日志流类型和日志偏移等补充元数据。

### 5.3 产生时机

- `instrumentTool` 在业务处理器执行前发布 `tool.started`。
- 业务处理器返回后发布 `tool.completed`，状态必须来自实际结果。
- Terminal Task 输出产生时发布 `command.output`。
- Changeset 准备、应用和文件变更完成时发布 `file.changed`。

事件先持久化成功，再发布到内存订阅器。持久化失败时不发布成功事件，并将错误记录到服务端日志。

## 6. 实时推送

### 6.1 传输方式

服务端提供本机只读 Unix Socket：

```text
~/.mcpx/run/workspace-observer.sock
```

Socket 文件使用仅允许服务运行用户访问的权限。观测端协议只支持订阅和接收事件，不暴露工具执行、审批、文件写入或其他变更操作。

### 6.2 订阅流程

1. CLI 连接 Socket，提交 workspace、历史数量和起始 sequence。
2. 服务端校验 workspace，并注册订阅者。
3. 服务端建立一个历史回放水位。
4. 先发送水位之前的历史事件。
5. 再发送水位之后的实时事件。
6. CLI 保存最后收到的 sequence，用于断线重连。

注册订阅者必须先于历史回放水位确定，避免历史回放期间发生事件而造成遗漏。事件推送使用全局 sequence，便于 workspace 内多个 Remote Session 合并排序和去重。

### 6.3 重连与补偿

- Socket 断开后 CLI 自动重连。
- 重连请求携带最后收到的 sequence。
- 服务端从持久化事件表补发缺失事件，再恢复实时推送。
- 服务端重启只影响连接，不影响已持久化的事件。
- SQLite 仅用于历史回放、断线恢复和序列补偿，实时路径完全由事件触发，不使用轮询。
- 订阅缓冲区溢出时发送 gap 标记，CLI 通过 sequence 补偿，不静默丢失。

## 7. 终端展示

默认 text 输出采用按时间排序的事件时间线：

```text
workspace: mcpx
observer: obs_xxx
mode: read-only / live

09:31:02  session=rs_xxx  INTENT
          定位测试失败原因并修复相关代码

09:31:02  TOOL  file_read
          input:
          path: internal/server/runtime.go

09:31:03  RESULT  file_read  OK  86ms
          output:
          ...

09:31:04  COMMAND  command_execute
          purpose: 运行相关测试
          $ go test ./internal/server/...

09:31:06  COMMAND OUTPUT  stdout
          ...

09:31:07  FILE CHANGES  2 files  +18 -4
          M internal/server/observability.go
          M internal/state/migrations.go

          diff:
          - old code
          + new code
```

展示约束：

- 工具开始事件明确展示模型意图和准备执行的输入。
- 工具结束事件展示实际状态、耗时、输出或错误。
- 命令 stdout 与 stderr 分开标识。
- 工具输入和输出使用结构化、可读的 JSON 或文本块展示。
- 文件变更逐文件展示路径、操作类型、增删统计和有限 unified Diff。
- Diff、日志或结果超限时显示明确的截断提示和资源 URI。
- 不根据缺失事件推测成功，不把未验证结果展示成已完成。

## 8. 脱敏与大小限制

默认观测策略为脱敏并限制大小：

- 递归识别并脱敏 `token`、`secret`、`password`、`authorization`、`cookie`、`api_key`、私钥等字段。
- 对命令参数、环境变量和 HTTP Header 做敏感字段处理。
- intent、工具输入、工具输出、命令日志和 Diff 使用独立大小上限。
- 超出上限时只保留安全范围内的前缀，并设置 `truncated: true`。
- 观测事件只保存脱敏后的内容，不保存通用工具输入输出的原始副本。
- 现有任务原始日志继续由既有文件权限和审计策略保护，观测端不会直接绕过这些策略输出原文。

## 9. 安全与边界

- Socket 为本机 IPC，默认使用运行用户权限控制访问。
- workspace 名称必须通过现有 Workspace Registry 校验。
- 观测命令不接受工具名、命令、审批 ID 或文件写入参数。
- 观测端的所有请求都只能转换为订阅请求。
- 观测进程不会在 workspace 目录创建、修改、移动或删除文件。
- 文件路径默认使用 workspace 相对路径，避免泄露不必要的宿主机绝对路径。
- 事件内容中的敏感值不能通过 intent、工具输入、命令输出或 Diff 绕过脱敏策略。

## 10. 失败、限制与未验证事项

- 未携带 intent 的旧客户端会被拒绝，需要同步升级客户端或模型调用约定。
- 工具未返回可观测输出时，终端显示“输出不可用”，不进行推测。
- 超限内容只显示截断结果，并保留截断标记。
- 服务端未运行、Socket 不可访问或 workspace 不存在时，CLI 必须输出明确错误原因和实际状态。
- 当前设计要求观测命令与服务端共享本机运行时目录；跨主机观测不在本次范围内。
- Unix Socket 在不同操作系统上的运行目录和权限细节需要实现阶段验证。
- Terminal Task 输出事件、Changeset 文件事件和通用工具事件的统一序列化需要实现阶段验证。

## 11. 验证计划

实现阶段至少覆盖：

- intent 解析、必填校验、长度限制和稳定错误码。
- 所有公开工具 schema 的 intent 必填声明。
- 脱敏器、截断器和命令输出分块逻辑。
- 事件持久化、workspace 聚合、全局 sequence 和去重。
- 事件持久化成功后才推送的顺序保证。
- Unix Socket 订阅、历史回放、实时推送、断线重连和 gap 补偿。
- CLI text、JSON、TTY 颜色和重定向输出。
- 工具失败、命令失败、测试失败、文件变更和 Diff 截断场景。
- `gofmt`、相关包测试、`go test ./...`、`go vet ./...`、竞态检测和构建。
