# ARC Read-only Agent Terminal 优化 PRD

> 版本：v0.1  
> 状态：Proposal  
> 日期：2026-08-09  
> 目标模块：`mcpx observe` / ARC human presentation  
> 产品定位：远程 Agent 的只读 Terminal Transcript  
> 非目标：交互式 Agent、PTY、checkpoint/task UI、日志平台

## 1. 背景

ARC 当前已经具备比较完整的远程 Agent 可观测基础设施：

```text
Agent Tool Execution
        │
        ▼
observationBridge
        │
        ├─ SQLite Store
        │    durable sequence
        │
        └─ Broker
             │
             ▼
        Unix Socket
             │
             ▼
        mcpx observe
             │
             ▼
        TextRenderer
```

代码上已经实现：

- SQLite `observation_events` 持久化；
- durable-first：事件先 `Store.Append`，再 `Broker.Publish`；
- Unix Socket JSONL live stream；
- observer reconnect 后通过 `after_sequence` 从 DB 恢复；
- server 先订阅 Broker，再读取 history，规避 history/live race；
- broker buffer 满时发 `gap`，observer 自动通过 DB 补齐；
- `TextRenderer` 已是 stateful renderer；
- terminal width / CJK display width 已有专门处理；
- EDIT 的 diff、行号、增删统计已经比较完善；
- command 能取得实际命令、stdout/stderr、exit code；
- read/search 能取得实际 path；
- `-detail`、`-format json`、diff mode、filter 已存在。

因此这轮优化不应该重做 DB、socket、event transport。

核心问题集中在：

> Human Presentation 层目前仍然过于 event/telemetry-centric，而目标应该是 action-centric 的只读 Agent Terminal。

### 1.1 主要代码依据

涉及的核心实现：

```text
cmd/mcpx-server/workspace.go
internal/observation/render.go
internal/observation/timeline.go
internal/observation/palette.go
internal/observation/event.go
internal/observation/store.go
internal/observation/broker.go
internal/observation/client.go
internal/observation/socket.go
internal/observation/human_snapshot.go

internal/arc/human.go
internal/arc/presentation.go

internal/server/observation_bridge.go
internal/server/observability.go
internal/server/tools_edit.go
internal/server/tools_workspace_delete.go
```

## 2. 产品目标

用户执行：

```bash
mcpx observe <workspace>
```

之后，应产生一种类似 coding agent 的终端体验，但用户只能观看，不能向 Agent 输入。

ARC 默认应该回答四个问题：

1. Agent 正在做什么？
2. Agent 实际操作了什么？
3. 操作结果是什么？
4. 为什么下一步发生了？

其中第 2 点优先级最高。

对于代码 Agent，必须可以明确审计：

```text
模型读取了哪些文件？
搜索了什么？
创建了哪些文件？
编辑了哪些文件？
删除/移出了哪些文件？
重命名了哪些文件？
执行了哪些实际命令？
命令结果如何？
```

## 3. 核心设计原则

### 3.1 Action facts > Agent narration

Agent 自述可以压缩，Agent 实际行为不能模糊。

禁止把：

```text
read(path=internal/server/foo.go)
```

仅表现为：

```text
• 正在检查相关实现
```

必须至少保留：

```text
• READ    internal/server/foo.go
```

### 3.2 默认视图不是 telemetry

以下信息继续保存在 Event / DB / JSON：

```text
operation_id
request_id
call_id
plan_id
plan_task_id
execution_task_id
sha256
duration_ms
phase
event sequence
```

但默认 terminal 不展示它们。

需要诊断时：

```bash
mcpx observe -detail mcpx
```

或：

```bash
mcpx observe -format json mcpx
```

再看。

### 3.3 不引入 checkpoint / percentage

ARC 不应显示：

```text
3/5 completed
60%
Phase 4
Next: README
```

除非这是 Agent 明确产生的事实。

远程 Agent 可以改策略、retry、fallback、临时扩大范围、并发操作。ARC 应表现已经发生的执行事实，而不是推断完成率。

### 3.4 Narration 与 Action 分层

推荐视觉结构：

```text
Agent narration

ACTION    concrete subject
          result

ACTION    concrete subject
          result
```

Narration 回答“为什么”，Action 回答“实际做了什么”，两者不可互相替代。

## 4. 默认视觉语法

建议把操作 vocabulary 固定下来：

| 类型 | 默认展示 |
|---|---|
| 文件读取 | `READ` |
| 搜索 | `SEARCH` |
| 新建 | `CREATE` |
| 编辑 | `EDIT` |
| 安全移出 | `MOVE OUT` |
| 真正删除 | `DELETE` |
| 重命名 | `RENAME` |
| 复制 | `COPY` |
| 命令执行 | `RUN` |
| 必要的外部调用 | `CALL` |

固定 verb 的价值是用户可以直接纵向扫描终端。

状态建议：

```text
• 普通成功/已完成
● 当前运行
! 可恢复异常 / tool failure
× 最终失败
? 等待确认（若 observer 可以看到）
```

不建议成功事件全部使用 `✓`，否则长 transcript 会产生大量视觉噪音。

## 5. READ 需求

这是本轮 P0 重点。

当前 `render.go` 已经能从 read input 取得 path，但多文件情况下存在类似：

```text
3 files (a.go, b.go, c.go...)
```

的摘要逻辑。这与 ARC 的审计定位冲突。

### 5.1 单文件

完整读取：

```text
• READ    internal/server/tools_edit.go
```

窗口读取：

```text
• READ    internal/server/tools_edit.go:301–370
```

如果能从 `offset + limit` 准确确定范围，应展示范围，不要让用户误以为模型读过整个文件。

### 5.2 Batch READ

一次 read 请求包含：

```text
a.go
b.go
c.go
d.go
```

必须保留所有路径：

```text
• READ    4 files
  ├─ internal/a.go
  ├─ internal/b.go
  ├─ internal/c.go
  └─ internal/d.go
```

不能：

```text
• READ 4 files (a.go, b.go, c.go...)
```

当前 `read` 自身批量最多 20 项，因此完整列出是可接受的。

### 5.3 重复读取

当前 `TextRenderer` 已支持重复读取 fold。这个能力可以保留，但限定为：

> 只有完全相同 path + range 的连续重复读取才允许 fold。

例如：

```text
• READ    src/demo.go:120–180
          repeated ×2
```

不能把不同 range 合并掉。

## 6. SEARCH 需求

SEARCH 与 READ 必须保持语义区别。

例如：

```text
• SEARCH  rg "workspace_move_out" internal/server
          5 matches in 3 files
```

随后：

```text
• READ    internal/server/tools_workspace_delete.go:220–300
```

这样观察者才能理解 Agent 搜到了什么，以及之后真正读取了什么。

当前 `render.go` 已经能够生成类似实际 `rg` / `find` 的 search representation，应继续使用。

## 7. EDIT / CREATE

用户已认可当前 diff 表现，因此本轮不重做 diff presentation，只优化外层信息结构。

当前：

```text
• Edited auth.go [-1,+1]
```

方向基本正确。

推荐最终：

```text
• EDIT    internal/server/auth.go                   -1 +1

    120 │ - old
    120 │ + new
```

CREATE：

```text
• CREATE  internal/server/foo.go                    +184
```

默认不再附带：

```text
purpose:
reasoning:
progress:
operation:
sha256:
format=UTF-8
line-ending=LF
final-newline=yes
```

除非它们与此次操作结果直接相关。

## 8. DELETE / MOVE OUT

当前安全删除实际使用 `move_out`，并非永久删除。

`tools_workspace_delete.go` 的提交结果已经产生：

```text
TypeFileChanged
Tool: "move_out"
```

而 payload 里已经有：

```text
target_preview[].path
target_preview[].kind
target_preview[].status
target_preview[].quarantine_path
moved_count
failed_count
reversible
quarantine_location
```

因此数据基础已经存在。

但是当前通用 `file.changed` renderer 主要围绕 EDIT 的 `results[]` 结构设计；Move-out 使用的是 `target_preview[]`，因此当前 presentation contract 并没有完全统一。

### 8.1 PRD 要求

不要把安全移出伪装成：

```text
DELETE
```

应该准确展示：

```text
• MOVE OUT  internal/server/legacy.go
            moved to managed quarantine
```

批量：

```text
• MOVE OUT  3 targets
  ├─ internal/legacy.go
  ├─ internal/old_test.go
  └─ fixtures/legacy/
             reversible
```

部分失败：

```text
! MOVE OUT  3 targets
  ├─ • internal/legacy.go
  ├─ • internal/old_test.go
  └─ × fixtures/legacy/ · MOVE_OUT_FAILED
```

只有未来存在真正永久删除事件时，才使用 `DELETE`。

这是审计语义，不只是文案问题。

## 9. RUN 需求

真实命令属于不可妥协信息。

当前代码已经能够获取：

```text
Event.Command
Event.WorkingDirectory
Event.ExitCode
Event.DurationMs
```

以及 tool input 的 command。

默认必须展示完整命令。

例如：

```text
● RUN     go test ./internal/server/... \
          -run 'MoveOut|WorkspaceDelete'
```

完成：

```text
• RUN     go test ./internal/server/... \
          -run 'MoveOut|WorkspaceDelete'              2.1s
          exit 0
```

失败：

```text
! RUN     go test ./internal/server/... \
          -run 'MoveOut|WorkspaceDelete'              2.1s
          exit 1

          TestMoveOutViaMCPProtocol
          acceptance_protocol_test.go:289
          move_out action branch is not discriminated
```

禁止用：

```text
• Running targeted tests
```

代替真实命令。

可以作为 purpose，但真实 command 永远是主信息。

## 10. stdout / stderr

当前实现将 command output 表现成：

```text
Read stdout
1 | ...
2 | ...
```

从“事件浏览器”角度合理，但从 Agent Terminal 角度略显突兀。

建议把 stdout 归属于对应的 RUN：

```text
• RUN     go test ./...
          │ ok internal/auth
          │ ok internal/server
          │ FAIL internal/mcp
          └ exit 1
```

而不是作为独立 Agent action：

```text
• Read stdout
```

因为从用户心智上，Agent 并没有执行“读取 stdout”这个工具动作，而是命令产生了 stdout。

第一阶段可以不改 streaming contract；底层继续使用 `command.output`，只改变 presentation grouping。

## 11. Semantic Context 优化

当前 ARC / observer 已经有：

```text
goal
purpose
reasoning_summary
progress_summary
next_step
plan_id
plan_task_id
execution_task_id
operation_id
```

`arc/human.go` 和 `timeline.go` 都会将这些组合成 Context，导致类似：

```text
purpose: ...
reasoning: ...
progress: ...
next: ...
plan: ...
operation: ...
```

频繁出现。

### 11.1 Default 模式

只允许两类 narration。

Purpose：在开始明显的新意图时：

```text
Checking the remaining move_out compatibility surface.
```

Progress / reasoning：只有发生策略变化时：

```text
The dispatcher behavior is correct. The remaining failure is isolated
to MCP schema discrimination.
```

普通工具操作不要重复 narration。

### 11.2 `-detail`

保留现在完整诊断能力：

```text
goal
purpose
reasoning
progress
next
plan
task
operation
duration
sha256
format
cwd
```

即数据不删，只改变默认层级。

## 12. 去掉当前 operation separator 的 telemetry 感

当前 `timeline.go` 的测试明确固定了类似：

```text
── execute · started · operation=op_progress · duration=27ms
```

这会让 observer 更像 trace viewer。

默认模式建议删除：

```text
started
operation=...
duration=...
```

改成实际 action：

```text
● RUN     go test ./...
```

完成：

```text
• RUN     go test ./...                              2.1s
```

其中 duration 建议：

- RUN：保留；
- 长时间动作：保留；
- 4ms、8ms 的 READ/EDIT：默认隐藏。

## 13. READ 的文件 metadata 下沉

当前测试要求默认输出：

```text
format=UTF-8
line-ending=CRLF
final-newline=yes
sha256=sha256:abc
```

这些对协议/debug 很重要，但对于日常 Agent terminal 属于高噪声。

调整为：

Default：

```text
• READ    demo.go
```

Detail：

```text
• READ    demo.go
          UTF-8 · CRLF · final newline · sha256:abc...
```

例外：如果 format 本身导致操作异常，则提升：

```text
! EDIT    demo.go
          file revision changed since last read
```

## 14. Recoverable failure

stale revision/context mismatch 往往是 Agent 正常 recovery 行为，不应和最终失败表现相同。

建议：

```text
! EDIT    internal/server/tools_workspace_delete.go
          revision changed; refreshing before retry

• READ    internal/server/tools_workspace_delete.go

• EDIT    internal/server/tools_workspace_delete.go
          -4 +7
```

真实过程没有隐藏，但严重级别得到正确表达。

最终无法恢复才：

```text
× EDIT    ...
          unable to apply after retry
```

## 15. 推荐默认最终效果

基于本次实际代码阅读场景，ARC 可以呈现为：

```text
Inspecting the ARC observer and its durable/live event path.

• READ    AGENTS.md

• READ    13 files
  ├─ internal/arc/arc.go
  ├─ internal/arc/human.go
  ├─ internal/arc/presentation.go
  ├─ internal/observation/render.go
  ├─ internal/observation/timeline.go
  ├─ internal/observation/palette.go
  ├─ internal/observation/socket_unix.go
  ├─ internal/observation/history.go
  ├─ internal/server/observability.go
  ├─ internal/server/observation_bridge.go
  └─ ...

The transport is already durable-first. The main optimization surface
is the human presentation layer rather than SQLite or the Unix socket.

• SEARCH  "func runObserve" cmd internal
          2 matches in cmd/mcpx-server/workspace.go

• READ    cmd/mcpx-server/workspace.go

• READ    internal/observation/event.go
• READ    internal/observation/store.go
• READ    internal/observation/broker.go
• READ    internal/observation/client.go
• READ    internal/observation/socket.go

Move-out already emits a durable file.changed event, but its target
payload differs from the edit results[] presentation contract.

• SEARCH  "TypeFileChanged" internal/server internal/observation
• READ    internal/server/tools_workspace_delete.go:301–490
• READ    internal/server/tools_edit.go:301–370
```

目标体验是：

> 看起来像 Agent 在工作，同时每个真实 filesystem / command action 都能被审计。

## 16. 实现拆分

### P0 — Default terminal 信息架构

目标：只修改 presentation，不改变 Event/DB/socket contract。

1. 默认移除 `operation/status/duration` separator。
2. Semantic Context 改为低频 narration。
3. READ 单文件显示完整 path。
4. Batch READ 完整列出所有 path。
5. READ range 可用时显示范围。
6. RUN 始终显示真实 command。
7. command output 视觉归属 RUN。
8. SHA / encoding / line ending 等移动到 `-detail`。
9. EDIT diff 行为保持不变。
10. 重复 READ 只允许 identical target/range fold。

建议最高优先级。

### P1 — Mutation semantic normalization

1. 为 `file.changed` presentation 增加明确 mutation projection：

```text
create
update
rename
move_out
delete
```

2. 给 `move_out target_preview[]` 单独 renderer。
3. 显示所有实际 moved/failed target。
4. 区分 `MOVE OUT` 和永久 `DELETE`。
5. partial move-out 按 target 状态展示。

这一阶段可能涉及 presentation DTO/normalizer，但不一定需要修改 durable Event schema。

### P1 — Failure/recovery presentation

定义：

```text
tool failure
recoverable conflict
partial success
final failure
agent/session stop
```

避免所有 `failed` 都使用同样视觉等级。

### P2 — Command output compression

在不影响 `-format json` 和 durable logs 的前提下：

- stdout/stderr 合并进 RUN block；
- 高频 bootstrap INFO 降权；
- failure 优先保留 tail / error lines；
- output budget 从“任意前 50 行”向“Agent terminal 摘要”优化。

这个可以后做，因为会涉及更多启发式。

## 17. 明确不做

本轮不建议：

- 不增加 PTY；
- 不做 stdin；
- 不做 tmux / ConPTY；
- 不做可交互 expand/collapse；
- 不增加 task checkpoint；
- 不计算任务完成百分比；
- 不重构 SQLite observation Store；
- 不重构 Unix socket；
- 不改变 JSONL 完整事件；
- 不重新设计 EDIT diff；
- 不让 ARC 根据事件猜 Agent plan。

## 18. 验收标准

### 18.1 操作透明度

对于一次 session：

- 每次文件 READ 都能确定 path；
- partial READ 能确定 range；
- batch READ 不遗漏路径；
- 每次 CREATE/EDIT/MOVE OUT/DELETE/RENAME 都能确定目标；
- EDIT 保留现有 diff；
- 每次 RUN 都显示完整真实命令；
- search 与 read 不混淆。

### 18.2 默认信息密度

默认 terminal 不再频繁出现：

```text
operation=...
request_id
plan_id
task_id
sha256
format
duration=8ms
purpose:
reasoning:
progress:
```

但 `-detail` / JSON 中仍可取得。

### 18.3 Narrative

连续工具操作中，不重复相同：

```text
goal
purpose
reasoning
progress
```

Narration 主要出现在：

- 意图切换；
- 测试结果导致策略改变；
- retry/fallback；
- 最终总结。

### 18.4 Reliability

不得破坏当前：

```text
SQLite durable sequence
durable-first publish
subscribe-before-history
after_sequence resume
broker gap recovery
JSONL format
filter semantics
terminal width wrapping
```

## 19. 代码层面的关键判断

本轮优化不需要从 ARC event schema 开始重做。

现有基础已经足够好：

```text
Event
  ↓
SQLite
  ↓
Unix Socket
  ↓
TextRenderer
```

真正缺少的是中间这个概念：

```text
Raw Observation Event
        ↓
Human Action Projection
        ↓
Read-only Agent Transcript
```

目前 `render.go + timeline.go` 已经承担了一部分 projection，但同时还残留较多 event lifecycle、semantic metadata、protocol diagnostics 直接进入 human output 的逻辑。

因此这次优化的核心可以定成一句：

> ARC 默认视图从 Event-centric Renderer 转为 Action-centric Agent Transcript；底层 Event 保持完整，文件和命令事实保持不可丢失。

## 20. 源码现状结论

基于当前代码，以下判断可作为实现前提：

1. `mcpx observe` 已支持 history replay、live tail、自动 reconnect、gap recovery，无需重构 transport。
2. Event 已具备 `phase`、`call_id`、`operation_id`、semantic context 等字段，无需为本轮默认展示新增基础生命周期字段。
3. `TextRenderer` 已是有状态 renderer，可承载 action grouping 和重复操作 fold。
4. EDIT 的 `file.changed.results[]` 与当前 diff renderer 对接良好，diff 本体无需重做。
5. MOVE OUT 同样发出 `file.changed`，并已具备具体 target/path/status/reversible/quarantine 数据，但 payload 使用 `target_preview[]`，和 EDIT 的 `results[]` presentation contract 尚未统一。
6. RUN 的 command/stdout/stderr/exit code 已有完整事实来源，主要工作是 presentation grouping，而不是增加采集能力。
7. 默认模式目前仍暴露较多 semantic/operation metadata，优化重点应集中在 human projection，而不是 durable event。
