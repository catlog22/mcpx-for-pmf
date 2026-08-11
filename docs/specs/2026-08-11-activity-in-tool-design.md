# Activity-in-Tool 设计

## 目标

让只支持 MCP tools/call 的客户端也能产生 Activity V3 语义轨迹；Activity 只通过会话内工具参数进入，不存在额外 HTTP ingress。

## 公共输入契约

所有公共业务工具增加可选 `activity` 对象：

```json
{
  "activity": {
    "intent": "...",
    "hypothesis": "...",
    "evidence": "...",
    "conclusion": "...",
    "next": "...",
    "status": "..."
  }
}
```

六个字段都是可选字符串，可以在同一次工具调用中同时赋值，但只应填写本次发生实质变化的语义，不重复上一调用中未变化的内容。`activity` 是工具输入层的聚合表示，不改变持久化事件模型。

字段语义：

- `intent`：当前工作 turn 的目标或要解决的问题。仅在新的实质工作 turn 开始时填写；不要用它描述单个工具动作。
- `hypothesis`：尚未被证据确认、可以被后续验证推翻的暂定判断。不得把已经观察到的事实写成 hypothesis。
- `evidence`：刚刚获得的可核验事实、代码现状、命令结果或其他直接观察。只写事实，不夹带由事实推导出的判断。
- `conclusion`：由已有 evidence 支持的当前稳定判断或问题结论。它是推断结果，不是原始事实，也不是行动计划。
- `next`：基于当前理解选择的立即下一动作；应与本次正在发起的 tool call 对齐，而不是描述更远期计划。
- `status`：无法归入前五类但值得公开的阶段、等待或阻塞状态变化。仅在状态实质变化时填写，不作为 heartbeat，也不替代 `progress` 的业务里程碑。

模型不得填写 `turn_id`、`sequence`、`state`、`related_call_id`。

## Runtime 展开

Runtime 在业务工具执行前读取 `activity`，按以下固定顺序展开非空字段：

1. `intent`
2. `hypothesis`
3. `evidence`
4. `conclusion`
5. `next`
6. `status`

每个非空字段形成一条独立 Activity V3 event，持久化仍使用单 `kind` + 单 `summary`。

Runtime 自动生成：

- `turn_id`：当前 Remote Session 的 runtime-managed activity turn。
- `sequence`：当前 turn 内严格单调递增。
- `state`：工具调用入口统一为 `preparing_action`。
- `related_call_id`：绑定当前 MCP tool call 的 request/call identity。

单次工具调用中的多个 Activity 共享同一 `turn_id`、`state` 和 `related_call_id`，sequence 按展开顺序递增。

## 生命周期边界

嵌入式 Activity 是唯一客户端入口。旧 `/mcp/activity` HTTP ingress、wire request、heartbeat 与客户端维护 sequence/state 的协议全部移除，不提供兼容路由。

Activity 必须在对应工具 observation 之前持久化，因此 observer 的顺序是“语义 → 工具 → 时间分割线”。

## 展示

保留现有 interaction 时间分割线。

示例：

```text
── Intent 追踪订单支付超时配置到自动取消任务的完整链路 ────────────
◇ Hypothesis 超时取消可能由订单定时任务驱动

• Read TradeOrderProperties.java

  ── 22:11:03 ────────────

◇ Evidence payExpireTime 从 fanyi.trade.order.pay-expire-time 注入
◇ Conclusion 订单支付超时由交易模块配置控制
◇ Next 继续确认自动取消任务如何消费该配置

• Read TradeOrderAutoCancelJob.java

  ── 22:11:05 ────────────
```

Intent 继续使用分割线样式；其他语义继续使用 `◇`。command、diff、Read、Edit 等证据展示不变。

## ARC

ARC V2 继续只引用服务端真实 Activity snapshot。工具输入中的聚合 `activity` 必须先通过 Runtime 接受和持久化，再由 ARC snapshot 读取；ARC 不直接信任或复制 raw tool input。

## 错误处理

- `activity` 非对象：schema 拒绝。
- 未知字段：schema 拒绝。
- 字段为空白：忽略，不生成 event。
- 字段超过 Activity summary 限制：工具调用返回明确 bad request，不执行业务 handler。
- Activity 持久化失败：工具调用失败，不允许出现“业务动作已执行但语义入口未记录”的半状态。

## 成功标准

- tools/list 中公共业务工具都有同一可选 `activity` struct。
- 一次调用可同时提交多个语义字段，并按固定顺序持久化。
- 模型无需维护 turn/sequence/state/call id。
- `observe(session).agent_activity` 返回最后一条拆分后的真实 Activity。
- observer 保留时间分割线，并在工具前展示本次输入 Activity。
- command/diff/read/edit 展示无回归。
