# 工作区观测终端交互块与颜色设计规格

状态：规格已获用户审查并批准，已完成实现

日期：2026-08-02

## 1. 背景与目标

`mcpx-server workspace <name>` 当前按事件逐条调用无状态文本渲染器。工具完成事件、Terminal Task 输出和文件变更事件虽然属于同一次操作，但终端中缺少统一边界，长输出也容易淹没后续交互。

本次改动为 text 模式增加按 MCP 工具调用归并的交互块，并按工具动作使用稳定颜色。观测事件仍按原有 sequence 持久化和推送，JSON 模式保持兼容。

## 2. 范围与非目标

### 范围

- 每次 MCP 工具调用形成一个交互块。
- 使用已有 `request_id` 关联 `tool.started`、`tool.completed`、`file.changed` 和命令输出。
- 增加块边界、动作颜色、总行数限制和长任务 continuation 展示。
- 保留 TTY、`NO_COLOR` 和 `--format json` 的既有行为。

### 非目标

- 不解析 `command_execute` 的具体 Shell 命令，不按 `go`、`git`、`npm` 等命令族细分颜色。
- 不新增 observation 数据库列，不改变 JSON 事件字段或事件顺序。
- 不在 text 输出中展示截断说明、Resource URI 或状态栏。
- 不改变命令执行、任务控制、文件修改和审批语义。

## 3. 事件关联与数据流

`tool.started`、`tool.completed` 已携带同一 `request_id`；`file.changed` 在变更请求中也已有该字段。当前 `command.output` 只有 task `operation_id`，需要在任务启动时传入请求 ID，并由 `Task`、`OutputChunk` 传递到 `observation.Event.RequestID`；同时保留 task ID 作为 `OperationID` 和日志偏移标识。

为 `command_execute` 和 `change_execute` 内部验证任务提供带请求上下文的任务启动入口，现有无观测调用继续使用原有入口。命令输出事件保留来源工具名，便于历史回放缺少 `tool.started` 时使用降级颜色和标题。`task_manage` 是独立 MCP 交互；它读取的后台任务输出仍归属于原始请求 ID。

## 4. 文本块渲染

CLI 使用有状态文本渲染器，按 `request_id` 管理最近最多 200 个交互；旧事件无请求 ID 时按 `operation_id` 或事件类型降级为独立块。现有无状态 `RenderText` 保留给单事件调用和兼容测试。

块的基本形式为：

```text
╭─ #42 · command_execute
│ • Ran go test ./internal/auth
│   ↳ 12 tests passed
╰────────────────────────
```

生命周期规则：

1. `tool.started` 只建立状态，不单独输出。
2. 第一条完成或输出事件创建块并输出顶部边界。
3. `command.output` 按 request ID 归入块；stdout/stderr 作为子行。
4. `tool.completed` 输出动作、进度和结果摘要；长任务后续输出使用同一请求 ID 的 continuation 块。
5. 新交互不覆盖旧块；事件按全局 sequence 原顺序追加。
6. 收到 gap 或重连时关闭未完成块并清除本地关联，后续未知事件独立展示。

每个块的正文预算为 20 个逻辑行，顶部和底部分隔线不计入预算。未超限时正文最多 20 行；超限时预留 1 行显示 `...`，实际正文最多 19 行。底部分隔线使用当前块的动作颜色；底部分隔线之后额外输出一个空行作为块间距，该空行不计入 20 行预算。不会输出“已截断”等文字，也不会输出 Resource URI。每个 continuation 块单独遵守 20 行正文预算。单行仍沿用当前最多 240 个 Unicode 字符的压缩规则。

## 5. 颜色规则

颜色仅应用于 TTY text 输出；禁用颜色时输出字符和换行结构不变。

| 来源工具或状态 | 颜色语义 |
|---|---|
| `command_execute` | 琥珀色 |
| `context_query` | 青色 |
| `file_read` | 蓝色 |
| `change_execute`、`file.changed` | 绿色 |
| 会话、工作区、进度事件 | 紫色或灰色 |
| 错误结果、错误 frame | 红色，覆盖动作颜色 |
| gap、重连、超限标记 `...` | 黄色 |

块边界和动作词使用来源工具颜色，结果子行使用低亮度颜色。Unified Diff 的新增行和删除行继续分别使用绿色和红色；原始命令文本不根据命令内容变色。

### 5.1 Unified Diff 视觉规则

Diff 文本按行识别，不改变持久化 Diff 内容：

- `+++ b/path` 使用绿色前景色；`--- a/path` 使用红色前景色。
- 以 `+` 开头的新增内容使用绿色前景色，并使用低饱和深绿色背景。
- 以 `-` 开头的删除内容使用红色前景色，并使用低饱和深红色背景。
- `@@` 块头和上下文行保持低亮度样式，不使用新增/删除背景。
- 每一行结束都显式重置前景色和背景色，不能污染后续文本或下一个交互块。
- 支持真彩色时使用 24-bit ANSI；背景色使用与深色终端背景预混合后的颜色模拟半透明效果。16 色终端降级为普通绿色/红色前景色，`NO_COLOR` 下完全不输出颜色控制符。
- 背景填充遵循当前终端可视宽度；发生折行时每个物理显示行都重新应用对应样式，不能造成左侧溢出或颜色串行。

该规则只作用于 TTY text 渲染。JSON、SQLite 观测事件、Changeset 原始 Diff 和已有 Resource URI 行为保持不变。

## 6. 失败与兼容处理

- 无法解析事件 JSON 时沿用当前通用事件摘要，不使观测进程退出。
- 缺少 request ID 或关联不存在时输出独立块，不猜测归属。
- 缓存达到 200 个交互后优先清理已关闭状态，无法清理时降级为独立块。
- `--format json` 继续一事件一行，保留现有 `truncated`、`resource_uri` 等字段；本规格只限制 text 展示。
- 非 TTY、`NO_COLOR` 和 stdout 写入错误继续由现有 CLI 路径处理。

## 7. 实现范围与测试

预计涉及 `internal/observation` 的事件关联与渲染、`internal/terminal` 的任务请求上下文、`internal/server/tools_command_execute.go` 和变更验证任务的请求传递、`cmd/mcpx-server/workspace.go` 的渲染器生命周期，以及对应测试和观测文档。

测试至少覆盖：请求 ID 传播；短命令与长任务输出；stdout/stderr；多个并行任务；20 行正文预算、分隔线、`...` 和底部空行；错误颜色覆盖；Diff 行限制；`+++`/`---` 前景色；新增/删除背景色与每行 Reset；真彩色、16 色和 `NO_COLOR` 降级；终端宽度与折行；历史缺少开始事件；gap 重连；旧 `RenderText` 行为；TTY、`NO_COLOR` 和 JSON 输出不变。

验收标准是：同一次 MCP 调用的可观测内容在终端具有一致颜色和块边界；正文不超过 20 个逻辑行，顶部和底部分隔线不计入预算；底部分隔线使用块颜色且其后有一个空行；超限只显示 `...`；无 Resource URI 或额外截断提示；JSON 与持久化事件兼容；相关 Go 测试、格式检查和静态检查通过。
