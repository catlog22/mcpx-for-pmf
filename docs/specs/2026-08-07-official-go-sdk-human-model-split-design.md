# MCPX：官方 go-sdk 迁移与人/模结果分流

- 记录日期：2026-08-07
- 状态：已实现（go-sdk v1.7.0；content/SC 分流；异步人读观测）
- 宿主约定：能调用 MCP 的 **GPT**（宿主 + 模型一体）；本地终端观测 **仅人看**，GPT **不读**观测流

## 1. 目标

1. 将 MCP 协议栈从 `github.com/mark3labs/mcp-go` **整仓迁移**到官方 `github.com/modelcontextprotocol/go-sdk`，并 **严格遵循**其工具结果与 Server/Transport 惯用法。
2. 统一 tools/call 返回：
   - **`content`**：给人 / UI 看的短摘要（Markdown 等）
   - **`structuredContent`**：给模型 / 程序用的结构化结果
3. 本地观测（终端时间线）只展示「GPT 想干啥、干了啥」，**异步**落库/广播，**不阻塞** tools/call，**不**作为模型第二输入通道。
4. 协议版本：**跟最新**（随官方 SDK 当前默认/最新协商），不为旧宿主做特判降级。

## 2. 非目标

- 不为 GPT 设计观测 API、订阅流或「模型可读观测」。
- 不把完整 `structuredContent` / ARC 信封塞进观测事件。
- 不把全部工具改成 `AddTool[In,Out]` 强类型 OutputSchema；公共工具统一声明精简 `outputSchema`，不在每个工具中重复展开 action 树和完整 ARC metadata。
- 不改 changeset / 命令等业务语义（仅协议层与结果/观测边界）。
- 不强制把确认流改成 MRTR elicitation（可后续单独立项）。

## 3. 概念：content vs structuredContent

| | content | structuredContent |
|---|---|---|
| 是什么 | 非结构化展示块（文本为主） | JSON 结构数据 |
| 给谁 | 人 / GPT 界面卡片 | 模型后续工具调用、判错、抄 id |
| 用法 | 读懂发生了什么 | **原样取字段**，禁止解析散文 |

一句话：**content = 人读摘要；structuredContent = 模型用的结构化结果。**

本地观测：只服务操作者旁观；与 GPT 是否读取无关。

## 4. 架构（保持简单）

```
tools/call
    → handler（业务）
    → 同步：组装 CallToolResult
         content            = 人读摘要
         structuredContent  = 统一机器契约
         _meta（可选）      = 全量 ARC / trace
    → 异步：投递本地观测快照（摘要/状态/命令等）
         Store.Append → Broker.Publish → 终端渲染
    → 立即 return（不等待观测写完）
```

硬规则：

1. 模型主契约是 **structuredContent**（标识从 `data`/`error` 复制）。
2. 观测只吃 **人读快照**，失败只打日志，不改变 tools/call 成败。
3. 禁止再以「纯 text 塞整包 JSON」作为合法主路径。

## 5. 统一结果契约

### 5.1 对外 CallToolResult（对齐官方 go-sdk）

| 字段 | 受众 | 要求 |
|------|------|------|
| `content[]` | 人 / UI | 短 Markdown / 状态 / diff；禁止整包 JSON dump |
| `structuredContent` | 模型 | 见 5.2 固定形态 |
| `tools/list.outputSchema` | 模型 / 客户端 | 描述 5.2 的公共结构；`data` 按 `type` 承载具体业务结果 |
| `_meta` | 调试 / 高级客户端 | 可放 `mcpx.result` 全量 ARC、trace |
| `isError` | 协议 | 业务失败可让模型自纠时为 true；`waiting_confirmation` 用 status 表达，不一律 isError |

官方语义参考：`CallToolResult.Content` 为非结构化结果；`StructuredContent` 为结构化结果（SEP-2106）；工具业务错误应进 result（`isError` + content），而非 JSON-RPC 级错误。

### 5.2 structuredContent 固定形态

```json
{
  "status": "succeeded | accepted | waiting_confirmation | interrupted | failed",
  "type": "text | code_change | log | error | plan | …",
  "context": {
    "goal": "",
    "purpose": "",
    "reasoning_summary": "",
    "progress_summary": "",
    "next_step": "",
    "plan_id": "",
    "task_id": ""
  },
  "data": {},
  "error": {
    "code": "",
    "message": "",
    "category": "",
    "retryable": false,
    "details": {},
    "recovery": {}
  },
  "hints": { "preferred_behavior": "" },
  "actions": []
}
```

- 可选字段在无信息时省略。
- `context` 是模型和人类 observer 共用的公开语义上下文；`reasoning_summary` 只能是简短判断依据，不是隐藏思维链。
- `session_id`、`changeset_id`、`expected_digest`、`confirmation_token`、`sha256` 等只出现在 `data`/`error` 中，由模型原样复制。

### 5.3 内部 handler 出口（ARC wrap 前）

仅两种 builder：

1. **成功**：`compactToolResult(data, humanSummary)`
   → text = 摘要；SC wire = `{ "status": "succeeded", "data": … }`
2. **失败 / 确认 / 中断**：`resultJSON(envelope.Response)`
   → text = 一句人读；SC wire = 公开 `{ status, data, meta?, error? }`

**禁止** builder 之后再执行 `result.StructuredContent = bareDTO` 覆盖 wire。

公开边界由 `arc.WrapToolResult`（或等价层）写成最终 `content` + 5.2 形态的 `structuredContent`。

`tools/list` 中 MCPX 工具的 `outputSchema` 与该 `structuredContent` 形态一致，只描述公共
包络字段：`status`、`type`、`context`、`data`、`error`、`hints`、`actions`。为控制远端
工具发现和传输体积，`data` 保持开放对象，由 `type` 和实际返回值共同确定业务字段；完整
ARC trace 不进入 `outputSchema`。

### 5.4 与 go-sdk 注册方式

- 使用 `mcp.NewServer` + `Server.AddTool` / 必要时 `mcp.AddTool`。
- 当前统一工具面继续 **显式** 构造 `CallToolResult`（动态 schema）；不强行本轮全量 typed `Out`。
- 字段语义必须与 SDK 一致：Content 展示、StructuredContent 结构、业务错误在 result 内。

## 6. 本地观测（仅人看、异步、简单）

### 6.1 目的

操作者在本机看到：GPT **想干什么、干了什么**。
GPT **不用、也不应**读观测流。

### 6.2 行为

- `instrument` 在返回前 **enqueue** 人读快照（started / completed），**不 await** 磁盘。
- worker：`Append` 成功后再 `Publish`（保持可恢复语义，但整段在后台）。
- 队列满或写失败：日志；**不改** tools/call 结果。
- 进程退出：有界 drain（如 2s），超时丢弃并记日志。

### 6.3 快照内容（刻意精简）

- tool、status、purpose/intent、人读 summary（可截断）
- command、cwd、exit_code、duration、关键 path（若有）
- 参数脱敏摘要（有界）

**不包含**：完整 structuredContent、ARC 信封、图片 base64、大 blob。

`NormalizeToolOutput` 改为只规范化人读快照；删除默认收录 `structured_content`。

## 7. SDK / 网关替换面

| 区域 | 现状 | 目标 |
|------|------|------|
| 依赖 | `mark3labs/mcp-go` | `github.com/modelcontextprotocol/go-sdk/mcp`（及需要的 auth 包） |
| Server | mark3labs MCPServer | `mcp.NewServer` |
| HTTP `/mcp` | mark3labs StreamableHTTP | 官方 `StreamableHTTPHandler` |
| 中间件 | 自研 instrument 包一层 | 保留薄包装；观测异步出站 |
| 测试客户端 | mark3labs client | 官方 `mcp.Client` + 对应 transport |
| 上游 MCP proxy | 基于 mcp-go | 官方 ClientSession |
| 协议 | 随 mcp-go | 跟最新 |

鉴权、OAuth、`~/.mcpx` 配置逻辑保留，只换协议栈挂载点。

## 8. 建议实现顺序

1. 统一 handler 出口 + 去掉 SC 覆盖 + 观测人读快照与异步（契约先正确）。
2. `go.mod` 切官方 SDK，删 mark3labs；适配 Content 类型差异。
3. Server + StreamableHTTP 挂载与鉴权接线。
4. 测试与 acceptance 客户端改官方。
5. mcpproxy 上游改官方 Client。

允许同一特性分支内按目录推进；验收以第 9 节清单为准。

## 9. 验收标准

| # | 标准 |
|---|------|
| A | `go.mod` 无 `mark3labs/mcp-go`；`go test ./...` 绿 |
| B | tools/call 始终有人读 `content` + 统一 shape 的 `structuredContent` |
| C | 协议/单测用 SC 断言业务字段，不把 prose JSON 当主契约 |
| D | 观测事件不含完整 structuredContent / ARC 信封 |
| E | 观测写路径不阻塞 tools/call 返回（完成路径不 await Append） |
| F | 本地时间线仍能展示工具名、status、摘要、命令/exit、关键 path |
| G | 最新协议下 Streamable HTTP 核心 acceptance 通过（session / change / plan） |

## 10. 风险（保持清醒、不复杂化）

- GPT 若只把 `content` 喂给模型、丢掉 SC，模型会退化成读摘要——属 **宿主转发**，MCPX 仍必须两边填对；agent_guidance 继续要求读结构化字段；确认 token 等可在 content 做人读兜底，**不是**主契约。
- 最新协议可能导致极旧客户端连不上——已选定「跟最新」，接受该取舍。

## 11. 决策记录

| 决策 | 选择 |
|------|------|
| 范围 | 一次到位：换 SDK + 结果契约 + 异步本地观测 |
| 方案 | B |
| 协议 | 跟最新 |
| 宿主 | 能调 MCP 的 GPT |
| 观测 | 仅本机人看；GPT 不读；异步、简单 |

---

实现前请审查本规格；确认后进入 `writing-plans` 拆实现计划。
