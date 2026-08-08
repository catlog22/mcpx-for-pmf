# MCPX 当前对外 Tool 目录历史记录

- 记录日期：2026-08-04
- 记录目的：在公共 Tool 重设计前冻结当前对外能力、参数分支和 Resource，作为实现后的完整性对照基线。
- 记录范围：MCP `tools/list` 暴露的 Tool、MCP Resource，以及公共调用的通用观测契约。
- 兼容性说明：本文记录当前状态，不代表新设计继续兼容这些名称或参数。

## 1. 当前公共 Tool

当前 `tools/list` 暴露 18 个 Tool。实际注册源是 `internal/server/tools_catalog.go`，能力清单由 `internal/server/capabilities.go` 同步维护。

| 序号 | Tool | 当前职责 | 分支或关键参数 |
| ---: | --- | --- | --- |
| 1 | `workspace_list` | 列出已注册 Workspace | 无 `action` 分支 |
| 2 | `session_open` | 创建或恢复 Remote Session，返回启动上下文 | `workspace`、`remote_session_id`、`label`、`description`、`client_request_id`、能力内容开关、`known_revisions` |
| 3 | `file_read` | 读取一个或多个 Workspace 相对路径 | `path`/`items`、`mode=window/full`、`offset`、`limit`、内容预算 |
| 4 | `context_query` | 搜索、列举并组装源码上下文 | `action=query/search/list`；搜索模式、路径范围、glob、分页、上下文行数、哈希和指令开关 |
| 5 | `change_execute` | 创建、应用或回滚文件 Changeset | `operations`；`changeset_id + expected_digest`；`revert_changeset_id` 三种互斥模式；`apply`、`format`、`verify`、`user_confirmed`、`idempotency_key` |
| 6 | `command_execute` | 执行命令或项目任务 | `command`/`task`、必填 `purpose`、`scope=workspace`、`yield_time_ms`、`user_confirmed` |
| 7 | `progress_report` | 记录模型可见的进度、结果和下一步 | `summary`、`result_summary`、`status`、`next_step`、`related_tool` |
| 8 | `session_manage` | 管理 Remote Session 生命周期 | `action=list/get/events/update/handoff/attach/close` |
| 9 | `change_manage` | 准备或查看 Changeset，不直接应用 | `action=prepare/diff/history` |
| 10 | `task_manage` | 管理命令和项目 Task | `action=attach/status/logs/list/stop/ports/diagnostics/stdin` |
| 11 | `plan_manage` | 创建和推进持久化 Plan | `action=create/get/start_task/complete_task/block_task/replan/deliver` |
| 12 | `runtime_inspect` | 查看运行时能力、项目摘要或指令 | `action=capabilities/project/instructions` |
| 13 | `environment_inspect` | 查看或保存环境快照并进行比较 | `sections`、`compare_to`、`save_snapshot` |
| 14 | `workspace_state` | 查看 Git 状态、快照、差异、变化和项目记忆 | `action=changes/snapshot/diff/watch/memory` |
| 15 | `extension_manage` | 发现或调用 Skill 和上游 MCP | `action=list/describe/call`；`kind=skill/mcp` |
| 16 | `artifact_manage` | 列出、读取或注册 Remote Session Artifact | `action=list/read/register` |
| 17 | `screenshot_capture` | 截取显示器或屏幕区域 | `mode`、显示器/区域坐标、压缩和输出尺寸 |
| 18 | `secrets_provide` | 提供仅驻留进程内的 Secret | `secret_id`、`values` |

## 2. 当前分支明细

### `session_manage`

| 分支 | 语义 |
| --- | --- |
| `list` | 查询 Session 列表，支持查询条件、状态、游标和数量 |
| `get` | 读取指定 Session |
| `events` | 读取 Session 事件，支持 `after_sequence` 和 `limit` |
| `update` | 更新标签、描述、状态和版本 |
| `handoff` | 创建一次性接力信息，设置角色、有效期和备注 |
| `attach` | 使用接力 Token 接入 Session |
| `close` | 关闭或归档 Session |

### `change_manage` 与 `change_execute`

`change_manage` 提供 `prepare`、`diff`、`history`；`change_execute` 提供 `operations`、`changeset_id + expected_digest`、`revert_changeset_id` 三种互斥模式。

文件操作类型：

```text
update
create
rename
delete
replace_exact
insert_before
insert_after
delete_exact
replace_range
```

文件版本使用 `base_sha256`；确认使用 `user_confirmed`；重复请求使用 `idempotency_key`。

### `task_manage`

```text
attach · status · logs · list · stop · ports · diagnostics · stdin
```

### `plan_manage`

```text
create · get · start_task · complete_task · block_task · replan · deliver
```

### `runtime_inspect`

```text
capabilities · project · instructions
```

### `workspace_state`

```text
changes · snapshot · diff · watch · memory
```

### `extension_manage`

```text
list · describe · call
```

`kind` 区分 `skill` 和 `mcp`；`call` 通过 `name`、`server`、`tool` 和 `arguments` 指定目标。

### `artifact_manage`

```text
list · read · register
```

## 3. 当前通用请求契约

公共 Tool 通过观测包装器补充并校验：

- 顶层 `intent`：必填、非空，最大 512 字节，用于说明目标和预期结果。
- `progress_summary`：可选，最大 512 字节，用于补发上一次已验证结果和下一步。
- `remote_session_id`：Session 作用域 Tool 的持久化 Session 标识。
- `workspace`：Workspace 名称，通常由 Session 解析。

`request_id` 和 `started_at` 由 Runtime 注入，不属于 MCP Tool 参数。内部请求会将业务参数归一化到 `Payload`。

## 4. 当前响应、错误与 Resource

内部响应包含 `ok`、`status`、`request_id`、Session/Workspace、各类耗时、`data` 和 `error`。错误结构包含：

```text
code · message · category · retryable · details
```

当前状态包括：

```text
ok · need_confirmation · need_secret · denied · unauthorized · error
```

错误恢复信息通过 `details.next_action` 或 `details.next_actions` 提供。

当前注册 3 个 MCP Resource Template：

| URI | 用途 |
| --- | --- |
| `mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}` | 读取 Artifact |
| `mcpx://remote-sessions/{remote_session_id}/changesets/{changeset_id}/diff` | 读取完整 Changeset Unified Diff |
| `mcpx://remote-sessions/{remote_session_id}/tasks/{task_id}/logs` | 读取完整 Task 日志 |

## 5. 当前观测与安全边界

持久化和推送事件类型：

```text
tool.started · tool.completed · command.output · file.changed · session.lifecycle · observer.notice
```

终端 `observe` 子命令支持 `--history 1..100` 和 `--format text|json`。text 模式使用紧凑行式 CLI 输出并压缩内部调度噪声；JSON 模式逐行输出完整事件。

当前安全边界包括：

- 文件修改统一通过 `change_execute`，不能由其他 Tool 绕过。
- 命令和文件分别受 `security.commands`、`security.files` 策略控制。
- Remote Session 受 Principal 和角色 ACL 控制。
- Secret 不写入 SQLite、日志、Workspace 或观测内容。
- 终端、事件和 Resource 具有脱敏、大小和截断边界。

## 6. 重设计对照规则

后续实现必须证明：

1. 18 个当前 Tool 的职责和每个分支都映射到新的公共入口。
2. 3 个当前 Resource Template 仍可访问，或由等价且更明确的 Resource 替代。
3. Session、Task、Plan、Changeset、Environment Snapshot、Workspace Memory、Artifact、Secret 和截图能力不减少。
4. ACL、命令/文件策略、版本校验、确认、幂等、错误分类和 Task 恢复语义不被绕过。
5. 当前 text/JSON 观测能力继续可用，并新增 Tool、命令、Skill 和 MCP 的父子调用追踪。
