# 批量查询异步操作结果设计

## 1. 背景

当前 `operation_batch` 用于提交带依赖关系的异步操作，`operation_manage` 用于查询、等待、读取结果、取消和恢复单个操作。`operation_batch` 禁止嵌套 `operation_manage`，因此模型在需要读取多个异步操作结果时会收到嵌套错误，只能退化为逐个调用。

本设计扩展现有 `operation_manage`，让模型可以在一个调用中读取多个已有操作的状态或结果，同时保持操作提交和操作观测的边界清晰。

## 2. 目标与非目标

目标：

- 支持一次查询多个已有操作的 `status` 或 `result`。
- 保留现有单操作调用、步骤结果读取和结果分页语义。
- 不增加公开工具数量。
- 对操作不存在、跨 Remote Session 访问、参数冲突给出严格错误。
- 让 `operation_batch` 的嵌套错误直接指向正确的批量查询方式。

非目标：

- 不允许 `wait`、`cancel`、`resume` 批量执行。
- 不允许 `operation_manage` 嵌套到 `operation_batch`。
- 不在本次设计中改变操作持久化、执行调度或结果聚合格式。

## 3. 公开接口

`operation_manage` 增加可选字段：

```json
{
  "operation_ids": ["op-1", "op-2"],
  "action": "result",
  "limit": 8192
}
```

参数规则：

1. `operation_id` 与 `operation_ids` 必须二选一。
2. `operation_ids` 必须是非空字符串数组，最多 32 个元素，且不能重复。
3. 使用 `operation_ids` 时，`action` 只能是 `status` 或 `result`。
4. 批量模式不接受 `step_id` 或 `cursor`；步骤级读取和分页继续使用单个 `operation_id`。
5. `limit` 在批量 `result` 中按每个操作分别生效，沿用现有默认值和最大值。
6. 使用单个 `operation_id` 时，现有所有动作和字段语义不变。

工具 schema 应明确表达单操作与批量模式的互斥关系，并把批量分支限制为 `status`、`result`；运行时仍需执行上述校验，不能只依赖客户端 schema 校验。

## 4. 返回结构

单操作返回结构保持不变。批量调用统一返回 `data.items`，元素顺序与输入 `operation_ids` 一致。

`status` 示例：

```json
{
  "action": "status",
  "items": [
    {
      "operation_id": "op-1",
      "session_id": "session-1",
      "workspace": "demo",
      "state": "running",
      "purpose": "读取源码",
      "steps": []
    }
  ]
}
```

`result` 示例：

```json
{
  "action": "result",
  "items": [
    {
      "operation_id": "op-1",
      "state": "succeeded",
      "steps": [],
      "result": {},
      "next_cursor": ""
    }
  ]
}
```

批量 `result` 读取每个操作的聚合结果，不读取指定子步骤。结果超过 `limit` 时，每个元素独立返回 `next_cursor`；模型需要继续分页时，按该元素的操作 ID 使用单操作 `operation_manage(action=result, cursor=...)`。

## 5. 校验与权限

服务端先完成整批校验，再生成任何返回数据：

- 所有操作 ID 必须存在。
- 所有操作必须属于当前 Remote Session。
- ID 不能重复，批量数量不能超过 32。
- 批量模式不能携带 `step_id`、`cursor`，也不能使用不支持的动作。

任一操作 ID 无效或越权时，整批失败，不返回部分结果。这样可以避免通过批量响应区分其他 Remote Session 中的操作，也保持单操作错误语义的一致性。

## 6. 外层状态聚合

`data.items` 始终包含每个操作的真实状态；外层 ARC 状态用于告诉模型下一步是否需要继续处理，按以下优先级计算：

1. 任一操作为 `waiting_confirmation`：返回确认状态。
2. 否则任一操作为 `queued` 或 `running`：返回 `accepted`。
3. 否则任一操作为 `failed`：返回错误状态。
4. 否则任一操作为 `interrupted` 或 `cancelled`：返回中断状态。
5. 全部为 `succeeded`：返回成功状态。

批量 `status` 和 `result` 都使用该聚合规则。确认令牌仍只出现在对应步骤的 item 中，`confirmation_token` 只表达语义确认，不承担认证职责。

## 7. 错误引导

`operation_batch` 继续拒绝嵌套 `operation_manage`，但错误消息改为明确说明：

```text
tool "operation_manage" cannot be nested in operation_batch; use operation_manage with action=status/result and operation_ids for batch queries
```

`operation_manage` 的工具描述和参数描述同步说明：批量查询直接调用自身的 `operation_ids`，不通过 `operation_batch` 包装。

## 8. 实现边界

- 在 `internal/server/tools_operation.go` 增加批量参数解析、整批权限校验、批量结果组装和外层状态聚合。
- 在 `internal/server/tools_catalog.go` 扩展 `operation_manage` schema 与模型提示，不新增工具注册。
- 复用 `operation.Service.Get` 和 `operation.Service.Result`，不新增数据库字段或迁移。
- 抽取单项状态/结果视图组装逻辑，确保单操作与批量操作使用相同的字段定义。
- 保持 `operation_batch` 的嵌套工具拒绝逻辑，只更新错误引导文本。

## 9. 测试计划

服务端测试覆盖：

- 两个已完成操作的批量 `status` 返回，顺序与输入一致。
- 两个已完成操作的批量 `result` 返回，每项包含正确聚合结果。
- 同一批次包含运行中、确认中、失败和成功操作时，外层状态按优先级聚合。
- `operation_id` 与 `operation_ids` 同时存在、都不存在、空数组、重复 ID、超过 32 个 ID 均返回 `bad_request`。
- 批量使用 `wait`、`cancel`、`resume`、`step_id` 或 `cursor` 均被拒绝。
- 不存在的 ID 和其他 Remote Session 的 ID 都整批失败且不返回部分数据。
- 批量结果分页返回每项独立的 `next_cursor`，单操作继续分页保持原行为。
- `operation_batch` 嵌套 `operation_manage` 时返回新的引导信息。
- 公开工具数量仍为 30，`operation_manage` schema 暴露批量分支。

## 10. 验收标准

模型面对多个 `operation_id` 时，可以直接通过一次 `operation_manage` 调用获得所有操作的状态或聚合结果；不会再尝试把 `operation_manage` 嵌套进 `operation_batch`。原有单操作查询、等待、取消、恢复、步骤读取和分页测试全部保持通过。
