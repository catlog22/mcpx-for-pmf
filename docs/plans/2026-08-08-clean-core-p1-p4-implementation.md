# MCPX 干净核心 P1–P4 总实现计划

> 状态：主线已完成，压力稳定性补丁已实现并进入回归收口。前置：[P0 实现计划](2026-08-08-clean-core-p0-implementation.md) 主线与交互补强均已完成；本文件记录 P1–P4 的实现、验收和后续稳定性证据，不自动 commit/push。

> 测试收缩：删除了与行为验收重复的 2 个纯单元测试（目的字段指纹快照、最终 catalog 快照）；保留真实 edit/幂等边界、HTTP acceptance、fake Skill/stdio MCP 和 plan/artifact 工作流作为证据，避免用测试数量替代覆盖质量。

## 生产级文件操作契约补充

- `read(view=file, mode=full)` 对源文件施加 `4194304` bytes 上限；超限返回 `FILE_TOO_LARGE/capacity`，window 读取走流式路径，不因源文件总大小失败。
- `read.items` 最多 `20` 项，超限返回 `LIMIT_EXCEEDED/validation`；`read(view=list, path=...)` 的 `path` 是硬作用域，`include_glob` 只能继续收窄。
- `STALE_REVISION` 重新读取并更新 `base_sha256` 后必须使用新的 `idempotency_key`；旧 key 继续绑定原始请求，避免幂等结果与新 revision 混淆。
- `edit(operation=delete)` 是 MCPX 封装的受审计文件删除能力：服务端先生成带 SHA 的待删快照并返回 `waiting_confirmation` 与 `confirmation_digest`，由客户端向用户展示范围并完成语义确认，再以相同业务参数设置 `user_confirmed=true` 重试。regular file 之外的目标和 symlink 均拒绝，删除由 `os.Root` 限定在 workspace 内。
- 删除请求不会绕过 Host 安全层：`edit` 的 MCP annotations 保留 `readOnlyHint=false`、`destructiveHint=true`、`idempotentHint=true`、`openWorldHint=false`，并在 `_meta["mcpx/safety"]` 与 `runtime_read` 能力摘要中发布审批所需的约束证据（Workspace 根目录、regular file、SHA、symlink 拒绝、幂等、审计和禁止 shell 绕过）。Host 应据此展示受约束的破坏性动作并要求用户批准；服务端仍独立执行语义确认和最终校验。
- 已发布机器可读限制：`read.max_source_bytes=4194304`、`read.max_items=20`、`operation_batch.max_steps=32`、`edit.max_changed_lines=1000`。

> 最终验证（2026-08-08）：`rtk go test ./... -count=1` 与 `rtk go test -race ./... -count=1` 均为 484 项通过；`rtk go vet ./...`、`CGO_ENABLED=0 rtk go build -o bin/mcpx-server ./cmd/mcpx-server`、格式检查和 `rtk git diff --check` 均通过。可复现场景命令见 [Evaluation](../evaluations/clean-core-p1-p4.md)。

## 压力稳定性补丁（2026-08-08）

压力评估发现 `operation_batch` 的 schema 校验在缺失 `type` 时执行空接口断言，导致 panic 穿透 MCP 请求 goroutine；同时需要保证任务输出观测和异步观测 recorder 的异常不会污染服务进程。

- [x] `validateOperationSchemaValue` 对 type-less / null schema 安全处理；`validateOperationToolArguments` 增加 schema 兜底，错误回到结构化 `bad_request`。
- [x] MCP tool handler / async submit 增加 panic 隔离，统一返回 `EXECUTION_RUNTIME_ERROR`，不向模型暴露堆栈。
- [x] operation worker 对单步 executor panic 做失败步骤收敛，保留无关步骤和后续 operation 的执行能力。
- [x] Task output sink、异步 observation recorder 增加 fail-soft 边界；补充 1 MiB stdout 排空回归。
- [x] source 缺失路径返回 `FILE_NOT_FOUND` / `not_found`，并消除 `statat` 原始错误文本泄漏。
- [x] 已通过 `go test ./... -count=1`、受影响包 race 回归、`go vet ./...`、`CGO_ENABLED=0 go build -o bin/mcpx-server ./cmd/mcpx-server`、gofmt 和 `git diff --check`；服务级高负载复测仍需重启服务后单独执行。

未宣称高负载外部复测已完成：需要重新启动服务后复跑阻塞 execute、高 stdout、cancel/resume，以及异常后的 observe、session attach、新 session open；demo 工作区中的 `mcpx-stress4/` 夹具仍需用户确认后再清理。

## 目标与冻结决策

P1–P4 把 P0 的 `session / read / edit / observe` 扩展为完整的开发闭环：执行命令、持续观察任务、记录轻量计划、保存产物、发现并调用 Skill/MCP，并以 Evaluation、能力声明和文档作为最终交付门槛。

### 最终公开工具边界

核心工作流工具固定为以下 10 个：

| 阶段 | 工具 | 公开职责 |
| --- | --- | --- |
| P0 | `session` | open / close / attach；跨客户端复用 `remote_session_id` |
| P0 | `read` | 文件、搜索、列表、上下文、环境读取 |
| P0 | `edit` | 新引擎批量文件变更 |
| P0 | `observe` | session、task、history、changes、logs、diff |
| P1 | `execute` | command / project task 执行，以及 task attach / stop / stdin |
| P2 | `plan` | 计划创建、读取、推进、完成、阻塞、重规划、交付 |
| P2 | `artifact` | 产物登记、列表、分片读取 |
| P3 | `discover` | Skill / 上游 MCP 列表与描述 |
| P3 | `skill_call` | 调用已由 `discover` 返回的 Skill |
| P3 | `mcp_call` | 调用已由 `discover` 返回的上游 MCP 工具 |

以下工具作为基础设施或宿主能力保留为 support tools，不进入主工作流：`operation_batch`、`operation_manage`、`runtime_read`、`environment_read`、`environment`、`screenshot_capture`、`secret_provide`。P4 统一它们的 `remote_session_id` 契约，但不把它们伪装成核心工作流工具。

下列旧公开名称在对应阶段完成后从 `tools/list`、session bootstrap、capability manifest、guidance、recovery action 和 README 一并移除，不提供公开别名：

`command_run`、`task_read`、`task`、`plan_read`、`extension_discover`、`artifact_read`。

### 跨阶段硬约束

- 所有有状态调用统一使用 `remote_session_id`；内部兼容字段只允许存在于执行器内部，不进入公开 schema 或恢复模板。
- `purpose` 是高风险调用必填字段；结果必须同时提供人类摘要和结构化数据。
- `idempotency_key` 按 `(remote_session_id, principal_id, key)` 隔离，并绑定规范化请求指纹；相同 key 的不同业务参数返回 `IDEMPOTENCY_CONFLICT`，不能静默复用旧结果。
- 幂等记录必须支持持久化、并发中的同 key 合并和重启后的状态恢复；不能只保存在 Runtime 进程内存中。
- 确认流程使用 `user_confirmed` 和服务端待确认状态；不再要求模型复制 `confirmation_token`、`changeset_id` 或 `expected_digest`。
- 所有长输出使用有界内联结果 + cursor/offset；变更 diff 也必须有界，完整 diff 通过 `observe(view=diff)` 分页读取，不把大 diff 隐藏到一次响应中。
- `execute`、`plan`、`artifact`、`discover` 等多动作工具使用 action-specific `oneOf`（或等价的互斥 schema），让远端模型只看到当前 action 的有效字段。
- 每阶段都必须同时更新：工具注册、能力声明、guidance、prompts、错误恢复、观测事件、协议验收测试。
- P1→P2→P3→P4 顺序执行；后一阶段不得依赖前一阶段的旧公开名称。

## P0 交互补强（P1 前置硬门槛）

P0 的精确 replacement、批量编辑、SHA 校验、格式保留和单文件原子写主线已经完成。以下补强属于 clean core 的交互与可靠性闭环，必须在 P1 公开契约继续扩展前完成；它们不改变 P0 的工具名称。

### 1. 大 diff：默认预览，按需分页

- `edit`、`observe(view=changes)` 和 `results[].diff` 默认只返回有界 Unified Diff 预览：单文件最多 32 KiB，整次响应最多 64 KiB；预算沿用现有 changeset 预览预算，按 UTF-8 字节计算。
- 响应必须同时返回 `edit_id`、`diff_bytes`、`diff_truncated`、`preview_bytes`；截断时提供 `next_action`，不得让模型猜测如何继续。
- 预览只能在 UTF-8 字符边界和完整 diff 行边界截断，并保留路径、hunk header、变更行统计和截断标记。
- 完整 diff 由 `observe(view=diff, edit_id, offset, limit)` 按字节分页返回；返回 `next_offset`、`eof` 和原始 `diff_bytes`。普通 edit 不为审阅完整 diff 强制增加调用，只有模型明确需要剩余内容时才继续读取。
- 观测与审计保留可恢复的完整 diff 或等价的受保护存储引用；普通 `history`、`changes` 查询不能因为历史事件而重新放大响应。

### 2. UTF-16：模型看文本，服务端保格式

- `read(mode=full|window)` 对带 BOM 的 UTF-16LE/UTF-16BE 文件先解码，模型侧 `content` 统一为可编辑的 Unicode 文本；响应标记传输 `encoding=utf-8`，同时在 `format.charset`、`format.bom` 中保留源文件格式。
- `sha256` 始终针对原始文件字节计算，不针对解码后的字符串计算；`offset`、`limit` 和 `total_lines` 按解码后的逻辑文本行计算，字节预算以响应文本为准并保证 UTF-8 边界。
- `edit` 的 replacement 和 full content 均接收解码后的文本，继续使用原始字节 SHA 做 `base_sha256` 校验，并按原字符集、BOM、混合换行和末尾换行重新编码写回。
- 未识别或损坏的 UTF-16 不得静默降级成可编辑文本；返回 `encoding=base64`、明确的 `UNSUPPORTED_ENCODING` 或 `BINARY_CONTENT`，并给出改用二进制读取的恢复动作。

### 3. 幂等：指纹、并发、重启都可解释

- 规范化请求指纹覆盖实际效果参数（路径、操作、replacement、内容、base SHA 等），排除 request ID、`idempotency_key`、`user_confirmed`、`purpose`、发现/执行路由元数据等重试、授权或审计字段；授权状态单独进入审计，秘密值不得写入明文日志或幂等表。
- 同一 `(remote_session_id, principal_id, idempotency_key)` 且指纹相同：首次请求执行，后续请求返回同一终态并标记 `idempotent_replay=true`；首次请求尚未结束时，后续请求等待或复用同一 in-flight 结果，不能并发写两次。
- 同一幂等键但指纹不同：立即返回 `IDEMPOTENCY_CONFLICT`，携带原指纹摘要、当前指纹摘要和可执行恢复动作，不返回原始业务参数。
- 幂等状态持久化为 `pending`、`succeeded`、`failed`、`in_doubt`；进程重启后，服务端根据预期新 SHA 对文件状态做 reconcile。全部目标已是预期状态时可安全补记成功；全部未变更时可继续执行；混合或未知状态返回 `IDEMPOTENCY_IN_DOUBT`，要求先 read/reconcile，禁止盲目重放。
- `edit`、`execute`、`plan`、`artifact`、`skill_call`、`mcp_call` 的写操作共用同一语义和测试矩阵；只读 `read`、`observe`、`discover` 不要求幂等键。

### P0 交互补强实现与验收

- [x] 抽出共享 diff preview / cursor 组件，接入 `edit` 和 `observe(view=changes|diff)`；补充超长单行、超大多文件和 Unicode 截断测试。
- [x] 抽出共享文本解码路径，接入 `read` 的 full/window/batch；补充 UTF-16LE/BE、BOM、代理对、混合换行、损坏输入和 edit round-trip 测试。
- [x] 新增持久化幂等存储、规范化指纹、in-flight 合并和重启 reconcile；补充 replay、conflict、并发、进程重启和批量部分状态测试。
- [x] 更新工具 schema、响应 DTO、错误恢复、observation、audit、guidance、prompts 和 capability revision。
- [x] P1 开始前验收：大 diff 默认响应不超过 64 KiB；UTF-16 可读且可编辑；幂等重试不重复写盘，冲突和不确定状态可恢复。

## 阶段依赖与交付门槛

```text
P1 execute + task observe
  ↓
P2 plan + artifact + evidence
  ↓
P3 discover + skill_call + mcp_call
  ↓
P4 Evaluation + capabilities + docs + final catalog audit
```

每一阶段都要求：实现 → 定向测试 → Streamable HTTP acceptance → 全量测试；阶段完成后才允许更新下一阶段的公开 schema。

---

## P1：`execute` 与 Task 观测

### 目标

把现有 `command_run`、`task_read`、`task` 合并为一个执行入口和一个观察入口：短命令直接返回结果，长命令返回 `execution_task_id`，后续只通过 `observe` 获取状态和日志。

### `execute` 契约

- `action=run`：`command` 与项目 `task` 二选一；返回 `exit_code`、stdout/stderr、duration；未结束时返回 `execution_task_id`。
- `action=attach`：必填 `execution_task_id`，按 offset 继续等待并读取输出。
- `action=stop`：必填 `execution_task_id`，执行权限和确认策略重新校验。
- `action=stdin`：必填 `execution_task_id`、`input`，只允许交互式执行任务。
- 公共字段：`remote_session_id`、`purpose`、`idempotency_key`、`execution_mode`、`yield_time_ms`、`scope`、`user_confirmed`。
- `execution_mode=async` 只表示工具调用进入异步 Operation；是否返回持久化 `execution_task_id` 由命令是否超过 `yield_time_ms` 决定。短命令可能在 Operation 内直接完成并返回 `completed_in_call=true`。
- `run` 必须限制为 Workspace scope；命令策略仍由服务端强制，禁止通过 shell 组合语法绕过策略。
- 首次命中 confirm 策略时返回 `waiting_confirmation`，只返回命令摘要和同一业务参数重试提示，不暴露 confirmation token。
- 命令执行失败使用 `COMMAND_NOT_FOUND`、`PROCESS_EXIT` 或 `EXECUTION_FAILED`；统一 `category=execution`，并在 `error.details.exit_code` 提供退出码。

### `observe` 扩展

- `view=status`：有 `execution_task_id` 时返回执行 Task 状态；无执行 Task ID 时返回 session 状态。
- `view=logs`：返回 stdout/stderr 分片、offset、next offset、EOF/truncated 和下一次调用模板。
- `view=history`：纳入 `execute.started`、`execute.completed`、`task.stopped` 等事件。
- 保持 P0 的 `view=changes`、`view=diff` 行为不变。

### 实现任务

- [x] 新增 `execute` clean handler，复用现有终端执行器和项目 Task 发现逻辑，移除公开 `command_run` 注册。
- [x] 将 Task attach/stop/stdin 控制并入 `execute`，移除公开 `task`、`task_read` 注册。
- [x] 扩展 `tools_observe.go` 的 status/logs 路由，统一 task 结果 DTO、offset 和分页错误。
- [x] 增加 execute 幂等记录：重试同 key 返回既有 task/result；参数冲突返回结构化 `IDEMPOTENCY_CONFLICT`。
- [x] 把 confirmation token 流程改为 `user_confirmed` + 服务端 pending digest，保留审计和策略重新检查。
- [x] 更新 operation async 入口，使内部 step 使用 `execute`，公开结果不泄漏 `command_run` / `task_manage`。
- [x] 更新 observation bridge、audit、recovery、session bootstrap、capabilities、guidance、prompts。

### P1 验收

- [x] `execute(command)` 短命令同步返回 stdout/stderr/exit_code。
- [x] 长命令返回 `execution_task_id`，`observe(status|logs)` 可跨请求、跨客户端继续读取。
- [x] stop/stdin 权限、确认、任务归属校验有效。
- [x] 重试、确认、命令拒绝、任务不存在、offset 越界均有结构化恢复动作。
- [x] `tools/list` 不再包含 `command_run`、`task`、`task_read`，且包含 `execute`。
- [x] 新增 P1 HTTP acceptance：run → observe status/logs → stop/complete。

---

## P2：`plan`、`artifact` 与 Evidence

### 目标

把计划和开发产物接入 P0/P1 工作流，使计划只引用结构化 evidence，不再要求模型管理 Changeset 或复杂 digest。

### `plan` 契约

`plan.action` 固定为：

`create | read | advance | complete | block | replan | deliver`

- `create`：返回服务端签发的 `plan_id` 和每个正式 `plan_task_id`；输入中的 `local_id` 只用于本次创建的依赖解析。
- `read`：按 `plan_id` 返回当前计划、任务状态、依赖、evidence 摘要。
- `advance`：启动一个可执行任务；只能引用返回的正式 `plan_task_id`。
- `complete`：必填 evidence；无验证证据不得完成任务。
- `block`：必填 reason，可附带已获得 evidence。
- `replan`：保留已完成任务和 evidence，只对未完成任务执行 add/update/remove。
- `deliver`：检查依赖、阻塞任务和 evidence 完整性，返回 ready、checks、blockers。

Evidence 允许 `read`、`edit`、`execute`、`artifact`、`verification`、`observe` 六类；公开 payload 禁止 `changeset_id`、`expected_digest` 和旧 Changeset URI。

### `artifact` 契约

`artifact.action` 固定为：`register | list | read`。

- `register`：登记 Workspace 相对路径，校验文件策略、类型、大小和 MIME；返回 `artifact_id`、sha256、format、size 和可选 Resource URI。
- `list`：按 kind、cursor、limit 列出当前 Remote Session 产物。
- `read`：按 byte offset/limit 分片读取；文本必须保持 UTF-8 边界，返回 `format`、`encoding`、`eof` 和下一次调用模板。
- 产物审计进入 observation；产物内容不进入普通工具日志，Resource 只作为大内容读取优化。

### 实现任务

- [x] 新增 clean `plan` handler，映射现有 plan store；将 `start_task/complete_task/block_task` 收敛为 `advance/complete/block`。
- [x] 将 plan delivery 的阻塞判断从 Changeset 草稿迁移到 edit/execute/artifact evidence。
- [x] 将现有 `plan_read` 从公开注册移入 `plan(action=read)`，清理旧恢复模板和 schema。
- [x] 新增 clean `artifact` handler，合并 `artifact` 与 `artifact_read`，保留 read/write 语义校验和 Resource link。
- [x] 为 artifact read 增加 UTF-8 边界、二进制、大小上限、cursor/offset 和 policy 测试。
- [x] 为 plan/artifact 写入 event、audit、history 和 `observe` 可见字段。
- [x] 更新 guidance/prompt/capability/bootstrap，所有推荐流程改为 `read → edit → execute → artifact → plan`。

### P2 验收

- [x] create → advance → edit/execute → complete → deliver 全链路可用。
- [x] 缺失 evidence、错误 `plan_task_id`、循环依赖、非法状态都有可恢复错误。
- [x] artifact register/list/read 支持文本、二进制和分片续读，策略拒绝不泄漏内容。
- [x] `tools/list` 不再包含 `plan_read`、`artifact_read`。
- [x] plan evidence 与 observe/history 中的 event ID、`plan_task_id`、`execution_task_id`、artifact ID 可相互追溯。

---

## P3：`discover`、`skill_call`、`mcp_call`

### 目标

把扩展发现和调用拆成清晰的两步：先发现并获得名称、schema、revision，再调用已发现对象；禁止凭记忆猜测 Skill、Server 或上游工具。

### `discover` 契约

- 替换 `extension_discover`，支持 `kind=skill|mcp`、`view=list|describe`、`query`、`name`、`server`、`include_tools`。
- Skill 返回 name、description、runtime、arguments_schema、permissions、revision、source 和 invocation 模板。
- MCP 返回 server 描述；`include_tools=true` 时返回上游 tool schema、tools_revision 和调用模板。
- 面向单个目标的 discover 结果必须附带 `discovery_id`、统一的 `discovery_revision` 和有效期/失效原因；调用方原样回传前两者。
- 结果只包含脱敏配置，不返回 command env 中的 secret。

### `skill_call` 契约

- 必填 `remote_session_id`、`name`、`purpose`、`arguments`、`discovery_id`、`discovery_revision`；名称和 revision 必须来自当前 Remote Session 的 `discover` 结果。
- 服务端按 manifest 的 `arguments_schema` 校验参数，并重新检查 Skill 权限、运行时和 Workspace 边界。
- 文档型 Skill 返回有界内容；可执行 Skill 返回 exit_code/stdout/stderr/duration，过大输出转为 task/artifact 读取路径。
- 支持 `idempotency_key` 和 `user_confirmed`；成功、失败、超时和拒绝写入 observation/audit。

### `mcp_call` 契约

- 必填 `remote_session_id`、`server`、`tool`、`purpose`、`arguments`、`discovery_id`、`discovery_revision`；server/tool 必须来自当前 Remote Session 的 `discover` 结果。
- 调用前重新加载 Workspace 合并配置、检查 MCP 开关和 allowlist；不允许通过参数覆盖 server command/env。
- 使用现有 stdio MCP client，设置连接、调用和结果大小上限；返回上游 structured result，同时隔离上游错误。
- 上游调用写入 parent/child observation 事件，错误返回 `MCP_DISCOVERY_STALE`、`MCP_CALL_FAILED` 等结构化 code。

### 强制 discover 状态机：额外调用成本直接暴露

- `session` bootstrap 中的 Skill/MCP 摘要、配置列表或历史 `discover` 结果不自动取得调用资格；每个 Remote Session 必须至少有一次显式 `discover`，并产生绑定 principal、Workspace、对象和 revision 的 `discovery_id`。
- `discover(kind=skill, name=...)` 必须一次返回目标 Skill 的 schema、权限、revision 和调用模板；`discover(kind=mcp, server=..., include_tools=true)` 必须一次返回目标 server 的 tool schema、revision 和调用模板，避免把一次合法链路拆成两次发现调用。
- `skill_call` / `mcp_call` 不得在服务端隐式触发 discover。缺少 discovery 或 revision 失效时，直接返回 `DISCOVERY_REQUIRED`；revision 不匹配时返回 `DISCOVERY_STALE` 或 `MCP_DISCOVERY_STALE`。
- 上述错误必须包含 `required_call_count=1`、`discovery_required=true` 和精确的 `next_action`（工具名及参数），让远端模型明确看到还需要一次 discover；不得只返回“名称未知”这类不可恢复文本。
- discover 结果在同一 Remote Session 内按 `(principal, Workspace, kind, object)` 缓存；revision 未变化时后续调用复用 `discovery_id`，配置或 manifest 变化立即失效并要求重新 discover。
- 合规链路固定为 `discover → skill_call/mcp_call`，即首次调用需要额外 1 次显式 discover；若模型跳过 discover，实际可见恢复链路为 `DISCOVERY_REQUIRED → discover → call`，共 3 次，成本不隐藏。

### 实现任务

- [x] 新增 clean `discover` 注册和 handler，移除公开 `extension_discover`。
- [x] 将 skill manifest 参数 schema 校验、revision 校验和权限校验放入调用前路径。
- [x] 将 MCP server/tool discovery 结果记录为 Remote Session 内请求级 revision，并在 mcp_call 中拒绝 stale/未知对象。
- [x] 新增按 Remote Session 隔离的 discovery lease/cache，校验 `discovery_id`、`discovery_revision`、principal 和 Workspace；禁止 call handler 内部隐式 discover。
- [x] 为缺少/过期/stale discovery 实现 `DISCOVERY_REQUIRED`、`DISCOVERY_STALE`、`MCP_DISCOVERY_STALE`，返回 `required_call_count=1` 和精确 `next_action`。
- [x] 补齐 mcpproxy 的连接超时、调用超时、输出预算、进程关闭和错误分类。
- [x] 为 Skill/MCP 调用补充父子 operation、observation、audit、脱敏和幂等字段。
- [x] 更新 skillItems、bootstrap、capabilities、guidance、prompts 中的 invocation tool 名称。

### P3 验收

- [x] discover → skill_call：schema 校验、成功输出、失败恢复和 stale name 拒绝。
- [x] discover → mcp_call：server/tool schema、revision、调用结果和上游错误均可验证。
- [x] 未 discover 的名称不能调用；缺少 discovery 时返回可执行的 `DISCOVERY_REQUIRED`，且不发生隐式 discover。
- [x] 按调用轮次验收：首次合规链路为 `discover → call` 两次；跳过 discover 为错误 → discover → call 三次；同一 revision 的后续调用只需一次 call；跨 Workspace、principal 或 revision 变化必须重新 discover。
- [x] `tools/list` 不再包含 `extension_discover`，且不存在旧名称恢复模板。
- [x] 使用本地 fake Skill 和 fake stdio MCP server 完成无网络集成测试。

---

## P4：Evaluation、能力声明、文档与最终收口

### Evaluation 交付物

新增 `internal/evaluation/`（或等价测试包）与 `docs/evaluations/clean-core-p1-p4.md`，使用本地 fixture，不依赖公网、真实用户凭证或宿主 UI。场景至少包括：

1. `session → read → edit → execute → observe` 基础开发闭环。
2. STALE、MATCH、POLICY、TOO_MANY_CHANGES、命令确认失败后的恢复。
3. 同一 `remote_session_id` 的跨客户端 attach 接力。
4. 多文件 edit、execute 长任务、offset/cursor 续读。
5. plan evidence：edit/execute/artifact 后 complete/deliver。
6. artifact 文本/二进制登记、列表、分片读取。
7. discover → skill_call 与 discover → mcp_call。
8. tools/list、session bootstrap、capability manifest、guidance 的名称一致性。
9. 大 diff 预览预算、`observe(view=diff)` 分页和 Unicode 边界。
10. UTF-16LE/BE read → edit → SHA/格式 round-trip。
11. 幂等 replay、参数冲突、并发合并、重启 reconcile 和 `in_doubt` 恢复。
12. 未 discover 的 Skill/MCP 调用返回一次明确的 `DISCOVERY_REQUIRED`，不触发隐式发现。

每个场景输出 pass/fail、工具轮次、恢复轮次、耗时、失败 code 和证据引用；生成物默认写入临时目录，不提交运行结果。P4 门槛是所有必选场景通过，且不得依赖猜测 ID、旧工具名或非结构化文本解析。

### 交互效率硬门槛

以下轮次统计包含模型可见的工具调用，不包含服务端内部函数调用；正常路径和恢复路径分开统计：

| 场景 | 门槛 |
| --- | --- |
| `session → read → edit` | 不超过 3 次；需要审阅最近变更时 `+ observe` 不超过 4 次 |
| 长命令启动并拿到首个有效状态或日志 | 不超过 6 次工具调用 |
| Skill/MCP 首次合规调用 | `discover → call` 恰好 2 次；同一 revision 的后续调用为 1 次 |
| Skill/MCP 跳过 discover 的恢复 | 结构化错误 → discover → call，最多 3 次，禁止隐藏内部调用 |
| 大 diff | 默认 edit 不因完整 diff 增加调用；只在模型请求剩余 diff 时分页 |
| 幂等重试 | 相同指纹不得产生第二次写入；冲突和 `in_doubt` 必须在 1 次错误响应内给出下一步 |

### 能力声明与文档

- [x] `capabilityToolNames`、实际 `tools/list`、session bootstrap、`runtime_read(view=capabilities)` 来自同一最终注册快照。
- [x] capability manifest 增加 core/support 分组、阶段版本、工具 schema revision、guidance revision、evaluation revision 和推荐工作流。
- [x] 清理 `agent.yaml`、prompts、recovery、README、resource 描述中的旧 P0/P1/P2/P3 工具名和 Changeset 工作流。
- [x] README 补充最终工具表、session 接力、read/edit/execute/observe/plan/artifact/discover 工作流、错误恢复和验证命令。
- [x] `runtime_read` 明确哪些工具是 core、哪些是 support，以及 capability disabled/forbidden 的原因。
- [x] 增加最终 catalog regression：旧名称不得出现在 tools/list、schema、bootstrap、guidance、recovery 和文档快照。

### P4 验收

- [x] Evaluation 必选场景全部通过并有可复现命令。
- [x] `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、CGO 关闭构建、gofmt、diff 检查全部通过。
- [x] Streamable HTTP acceptance 覆盖最终 core 工具及跨客户端 session 接力。
- [x] 最终公开 core 工具集合与本文件冻结表一致；support tools 不泄漏旧工作流字段。
- [x] 计划、规格、README、guidance、prompts 和 capability revision 相互一致。

---

## 统一文件变更范围

优先沿现有领域文件修改，必要时新增：

- `internal/server/tools_clean_core.go`、`tools_execute.go`、`tools_observe.go`、`tools_plan.go`、`tools_artifact.go`、`tools_discover.go`。
- `internal/server/tools_catalog.go`、`tools_public_adapters.go`、`operation_runtime.go`、`tools_operation.go`。
- `internal/server/capabilities.go`、`agent_guidance.go`、`guidance/agent.yaml`、`prompts/tools.yaml`、`observation_bridge.go`。
- `internal/server/diff_preview.go`、`internal/server/idempotency_store.go` 或 `internal/state/` 中的持久化幂等与 diff 分页适配。
- `internal/plan/`、`internal/artifact/`、`internal/mcpproxy/`、`internal/skill/` 的契约适配与边界测试。
- `internal/server/*_test.go`、协议 acceptance、`internal/evaluation/`、`docs/evaluations/`、`README.md`。

## 执行与收尾规则

- 每阶段开始前先确认上一阶段验收证据；不得跨阶段混改公开契约。
- 每个阶段完成后回写本文件 checkbox、验证命令和未决风险。
- 保留用户现有未提交改动，不使用 reset/checkout 覆盖；手工编辑使用 `apply_patch`。
- 不自动 commit/push；全部 P1–P4 完成后只展示摘要、验证证据和建议 commit message，等待用户明确确认。
