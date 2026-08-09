# MCPX 三端终端观测实现计划

> 状态：已完成（2026-08-08）。范围是普通管道、持久化事件、JSONL 传输和三端渲染；不引入 PTY、tmux 或 ConPTY。
> 关联：[P1–P4 总实现计划](2026-08-08-clean-core-p1-p4-implementation.md)。

## 后续增量：人类终端紧凑渲染

本轮在既有事件契约上收敛 text 输出，不改变 JSONL 和 durable event：

- [x] 改为紧凑行式 CLI 输出，移除大块交互边框和 `continued` 噪声。
- [x] 静默成功的 `operation.*` 调度事件及重复远端 started/completed notice，保留失败/取消结果。
- [x] ARC 人类展示按核心工具和状态使用语义颜色；相邻操作块显示带工具/状态的细分隔线。
- [x] ARC 与三端观测统一提供 `goal`、`purpose`、`reasoning_summary`、`progress_summary`、`next_step`、`plan_id`、`task_id`、`operation_id`；终端按三组 Context 展示且不重复打印。
- [x] 文本 diff 在动作行显示聚合 `(+N -N)`，文件行继续显示统计，逐行保留新增/删除颜色。
- [x] 文本输出清理 ANSI/C0 控制字符；`-format json` 保留结构化事件内容。
- [x] 保留 stdout/stderr、行号、宽度换行、输出预算和 `-detail` 诊断开关。

## 目标

让模型、终端和后续 Web 客户端消费同一套可恢复事件：

- 模型只提供并看到可公开的语义 Context；`reasoning_summary` 仅记录简短判断依据，不记录或暴露隐藏思维链。
- Runtime 事实使用统一的 `phase` 表示 `thought_summary`、`action_started`、`output`、`result`、`error`。
- `request_id` 是 MCPX 内部请求关联键；事件同时输出 `call_id`，有外部 call ID 时保留它，否则回退为 `request_id`。
- 不引入 room/房间概念；不同客户端通过 `remote_session_id` 接力，并可结合 Workspace 查询历史操作；真正的执行身份仍是 `remote_session_id + operation_id/task_id`。
- 任务继续使用普通 stdout/stderr pipe；原始任务日志作为大输出恢复源，观测事件只保存脱敏、有界摘要和偏移。
- SQLite observation Store 是断线回放的 durable source；本地 socket 和 `observe --format=json` 使用一行一个事件的 JSONL 帧，终端、Web 和脚本不需要各自实现另一套协议。

## 实现边界

### 1. 事件契约

- 给 `observation.Event` 增加 `phase`、`call_id`。
- 给 `envelope.Request` 接收可选 `call_id`；缺少 `call_id` 时由观测边界用 `request_id` 补齐。
- 给存储、历史查询 DTO、JSONL 和文本渲染保留同一组字段。
- `tool.started`、`command.output`、`tool.completed` 分别映射动作开始、输出和结果/错误阶段；其他 Runtime 事件按类型归类。

### 2. Durable-first 与效率

- `observationBridge.Record` 先完成脱敏和 Store.Append，拿到正数 sequence 后才 Broker.Publish；不再向 live 流发送负 sequence 的临时副本，也不产生 live/durable 重复事件。
- 观测写入使用脱离请求取消的短预算，工具生命周期和任务输出均走有界异步队列，不把 SQLite 延迟放到 MCP 请求或 pipe 复制热路径。
- 队列溢出时保留 Task 原始日志恢复路径并输出明确的 observer 丢失告警；不能把丢失伪装成已持久化。
- 关闭流程先 drain 异步事件，再关闭 socket、broker 和数据库。

### 3. 三端消费

- JSONL：输出完整结构化事件，适合作为 Web/脚本协议和断线重放输入。
- text：显示阶段化的动作、脱敏进度摘要、命令输出、退出码和结果；不显示模型私有思维链或协议诊断噪声。
- model：继续通过工具结果和 `progress_summary` 获得摘要；不新增读取隐藏观测日志的模型 API。

## 具体任务

- [x] 增加事件 phase/correlation 字段、SQLite migration、Store 读写和历史 DTO。
- [x] 修正 bridge 的 durable-first 顺序，统一 phase/call/room 填充。
- [x] 将任务输出观测移入异步队列，保留 Task 日志和恢复提示。
- [x] 更新 JSONL/text 渲染和最小充分回归测试。
- [x] 将语义 Context 接入 ARC `structuredContent.context`、ARC metadata、JSONL、history、持久化 Store 和人类 text observer。
- [x] 通过 `tools/list.outputSchema` 公布精简的 ARC `structuredContent` 契约；数据字段保持开放，避免每个工具重复展开完整结果定义。
- [x] 运行格式检查、相关包测试、全量测试、race、vet 和 CGO 关闭构建。

## 验收标准

1. 任何成功推送给 live observer 的事件都已有正数 durable sequence，历史回放不会缺失或重复同一 bridge 写入。
2. `tool.started → command.output → tool.completed` 能按 `call_id`、`request_id`、`operation_id` 和 `phase` 关联；失败完成事件为 `phase=error`。
3. 语义 Context 在 ARC、JSONL、历史查询和 text 渲染中可见，且超长内容仍有界、已脱敏；text observer 不重复显示同一操作的 Context。
4. stdout/stderr 仍不依赖 PTY，在 Unix/Windows 代码路径上保持普通 pipe；任务日志仍可用 offset/resource 恢复。
5. 终端、JSONL 客户端和模型面对同一执行事实；不同客户端可使用 `remote_session_id + workspace` 查询历史并接力，不需要猜测额外上下文 ID。
6. `tools/list` 的每个 MCPX 工具均声明与实际 `structuredContent` 一致的 `outputSchema`，且 Schema 只描述公共包络，不重复内嵌 ARC metadata 或大段工具专属定义。

## 验证记录

实现完成后把命令与结果补在此处；不新增与现有行为验收重复的测试，仅覆盖本计划引入的排序、字段和异步边界。

- 定向：`rtk go test ./internal/envelope ./internal/observation ./internal/server -count=1`，228 项通过。
- 全量：`rtk go test ./... -count=1`，485 项通过。
- 竞态：`rtk go test -race ./... -count=1`，485 项通过。
- 静态与构建：`rtk go vet ./...`、`CGO_ENABLED=0 rtk go build -o bin/mcpx-server ./cmd/mcpx-server` 通过。
- 格式与差异：`gofmt -l ./cmd ./internal`、`rtk git diff --check` 无输出。
