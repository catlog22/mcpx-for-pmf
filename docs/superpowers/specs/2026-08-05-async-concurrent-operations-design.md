# 公开工具异步并发与批量 DAG 设计规格

- 日期：2026-08-05
- 状态：草案，待用户审查
- 范围：MCP 公开工具、异步操作、批量依赖调度、终端观测

## 1. 目标

为网页端模型提供统一的异步与并发能力，减少独立工具调用的串行等待，同时保证文件变更、命令执行、语义确认和观测记录具有严格、可追踪的语义。

本设计覆盖两类能力：

- 多个独立公开工具调用可以并发执行。
- 单次 `operation_batch` 可以提交带依赖关系的多个子操作。

不改变现有 `task_read`、`task_control` 对终端任务的职责。终端任务是操作的一种执行载体，不能替代通用操作生命周期。

## 2. 公开接口

当前保留 28 个公开工具，新增 2 个工具后总数为 30 个：

| 工具 | 职责 |
| --- | --- |
| `operation_batch` | 提交多个公开工具操作，并按依赖关系调度 |
| `operation_manage` | 查询、等待、读取结果、取消或恢复异步操作 |

### 2.1 普通工具的异步模式

普通公开工具增加可选字段：

```json
{
  "execution_mode": "sync"
}
```

取值只有：

- `sync`：同步执行，默认值。
- `async`：创建后台操作并立即返回 `operation_id`。

异步模式只改变等待方式，不改变工具的业务参数、权限检查、确认要求和审计规则。

### 2.2 `operation_batch`

批量操作使用固定结构。子操作的 `arguments` 只包含目标工具的业务参数；`session_id` 和 `purpose` 从批量请求继承，避免同一批次出现多个会话或多个语义目的。

```json
{
  "session_id": "rs_xxx",
  "purpose": "分析医生端和药师端实现差异",
  "operations": [
    {
      "id": "search_doctor",
      "tool": "source_read",
      "arguments": {"view": "search", "query": "doctor"},
      "depends_on": []
    },
    {
      "id": "search_pharmacist",
      "tool": "source_read",
      "arguments": {"view": "search", "query": "pharmacist"},
      "depends_on": []
    },
    {
      "id": "read_files",
      "tool": "source_read",
      "arguments": {"view": "file", "path": "docs/README.md"},
      "depends_on": ["search_doctor", "search_pharmacist"]
    }
  ]
}
```

约束：

- `id` 在批次内唯一，只允许字母、数字、下划线和短横线。
- `tool` 必须是当前已注册的公开工具。
- `depends_on` 只能引用同一批次内的 `id`。
- 依赖图必须无环。
- `depends_on` 只控制执行顺序和失败传播，不自动把前置结果注入后置参数；需要动态路径时，先读取前置结果，再提交下一批次。
- 服务端在启动任何子操作前，完成所有工具名、参数、会话权限和依赖关系校验。
- 批次默认异步执行，返回总的 `operation_id`。

### 2.3 `operation_manage`

```json
{
  "session_id": "rs_xxx",
  "operation_id": "op_xxx",
  "action": "wait",
  "timeout_ms": 30000
}
```

`action` 取值：

| action | 语义 |
| --- | --- |
| `status` | 返回操作和所有子操作的当前状态，不等待 |
| `wait` | 最多等待指定时间；完成则返回结果，超时则返回当前状态 |
| `result` | 读取已完成操作或指定子操作的完整结果，支持游标分页 |
| `cancel` | 取消尚未完成的操作；运行中的命令尝试终止对应进程 |
| `resume` | 使用语义确认令牌恢复等待确认的子操作 |

`operation_id` 是持久化查询键。操作完成后仍保留结果，直到操作保留策略清理。

## 3. 响应语义

异步提交立即返回：

```json
{
  "status": "accepted",
  "data": {
    "operation_id": "op_xxx",
    "state": "queued"
  }
}
```

等待完成后返回：

```json
{
  "status": "succeeded",
  "data": {
    "operation_id": "op_xxx",
    "state": "succeeded",
    "result": {}
  }
}
```

`wait` 超时不会取消操作，只返回：

```json
{
  "status": "accepted",
  "data": {
    "operation_id": "op_xxx",
    "state": "running",
    "next_action": "operation_manage.wait"
  }
}
```

批量结果包含每个子操作的 `id`、`state`、`result` 或 `error`：

- 独立分支失败时，其他无依赖分支继续执行。
- 依赖失败时，后续子操作为 `skipped`。
- 结果过大时，响应返回摘要和 `cursor`，通过 `action=result` 分页读取。

## 4. 生命周期

内部状态：

```text
queued → running → succeeded
                 ├→ failed
                 ├→ waiting_confirmation
                 ├→ interrupted
                 └→ cancelled
```

依赖失败的子操作使用 `skipped`，不视为自身执行失败。

公开响应状态继续使用统一状态：

- `accepted`：已接收，处于排队或运行中。
- `waiting_confirmation`：等待用户语义确认。
- `succeeded`：操作或批次成功完成。
- `interrupted`：被用户取消或请求上下文中断。
- `failed`：执行失败、依赖失败或结果读取失败。

## 5. 调度与并发边界

调度器根据工具注册的注解和工作区范围选择执行槽位：

- 只读、幂等工具：同一工作区内允许并行。
- 不同工作区的写操作：允许并行。
- 同一工作区的写操作：默认独占执行，避免文件状态交叉污染。
- `command_run`：默认按可能修改工作区处理；长命令由现有 `Task` 承载。
- 未知或开放世界工具：默认独占执行，除非显式声明可并发。
- 超过进程级、工作区级并发上限时进入 `queued`。

调度器不依赖模型是否能够在同一轮主动发起并行调用。网页端模型即使只能串行发起 MCP 调用，也可以通过 `operation_batch` 显式提交并行分支。

已完成的写操作不提供自动回滚。文件变更继续使用 Changeset、摘要确认、版本校验和冲突检测保护。

## 6. 确认与恢复

批次中的每个需要确认的子操作独立进行语义确认：

- 不需要确认的只读分支可以继续执行。
- 需要确认的子操作及其后继依赖进入 `waiting_confirmation`。
- 响应返回该子操作的确认摘要、操作摘要和 `confirmation_token`。
- `operation_manage(action=resume)` 携带同一令牌恢复该子操作。
- `confirmation_token` 只表示用户已经确认，不承担认证职责。
- 服务端恢复时重新校验会话权限、操作摘要和工作区状态，不能仅凭令牌绕过安全策略。

## 7. 持久化与观测

新增持久化操作记录，至少包含：

- `operation_id`、`parent_operation_id`、`step_id`。
- 工作区、会话、请求和工具标识。
- 操作状态、创建时间、开始时间、完成时间和错误信息。
- 批次定义、依赖关系、每个子操作的结果引用。
- 过期时间和结果清理状态。

观测事件新增操作层级：

```text
operation.started
  ├─ operation.step.started
  │   ├─ tool.started
  │   └─ tool.completed
  └─ operation.completed
```

每个实际调用继续记录：

- 公开工具名称。
- 实际执行的命令和工作目录。
- 调用的 Skill 名称。
- 调用的 MCP Server 和 MCP Tool。
- `operation_id`、`step_id`、`parent_operation_id`。

并发事件的数据库序列只表示持久化顺序，不表示实际执行先后。实际时序通过开始时间、完成时间和父子关系还原。

## 8. 错误、取消与重启

- 批次预校验失败：整个批次不启动任何子操作。
- 排队中的操作取消：直接标记为 `cancelled`。
- 运行中的普通操作取消：向执行上下文发送取消信号。
- 运行中的命令取消：调用现有 Task 终止机制，并记录终止结果。
- 已完成操作取消：返回不可取消错误，结果保持不变。
- 进程重启后：从持久化记录恢复未完成操作；无法安全恢复的操作标记为 `interrupted`，不得自动重放写操作。
- 结果读取必须幂等，不重复执行原操作。

## 9. 验收标准

- 4 个无依赖只读子操作可以并行，整体耗时接近最慢分支而不是所有分支耗时之和。
- 有依赖的子操作不会提前执行。
- 环路、重复 `id`、未知工具和无效参数在执行前被拒绝。
- 同一工作区的写操作不会同时进入执行阶段。
- 独立分支失败不阻塞无依赖分支，失败分支的后继操作被跳过。
- `async` 调用可以通过 `status`、`wait` 和 `result` 获取最终结果。
- `wait` 超时不会隐式取消操作。
- `cancel`、`resume` 和 `confirmation_token` 语义符合安全策略。
- 重启后可以查询已持久化操作，未安全恢复的写操作不会自动重放。
- 历史查询可以按 `operation_id`、`step_id`、工具、命令、Skill 和 MCP 调用定位完整链路。
- 竞态检测、全量单元测试、静态检查和构建通过。

## 10. 范围外内容

- 不支持自动回滚已经完成的多个写操作。
- 不根据模型自然语言自动推断操作依赖，依赖必须由批量请求明确提供。
- 不允许批次中的子操作跨越不同 `session_id`。
- 不将 `operation_id` 作为权限凭证。
