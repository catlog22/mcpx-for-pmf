# MCPX：生产级干净核心 Runtime 设计

- 记录日期：2026-08-08
- 状态：设计已确认（待实现计划）
- 方案：干净核心重置（Clean Minimal Core）
- 优先级：**A — Agent / 模型成功率与体验优先**
- 宿主：ChatGPT / Claude / Cursor / Grok 等 MCP 客户端；本机终端观测仅人看

## 1. 目标

1. **Agent 成功率优先**：让 LLM 以高成功率、低仪式感完成真实开发任务（读、改、跑、验证、接力）。
2. **极简心理模型**：模型主要记住 `workspace + remote_session_id`；其余标识由服务端管理或从上一步返回直接复制。
3. **干净重置**：无向后兼容、无迁移路径；旧工具名、旧流程、旧 ID 语义全部废弃。
4. **高效 edit**：全新轻量实现，不复用现有 changeset / journal / partial-apply 路径。
5. **可观测对齐**：变更 diff 摘要同时给客户端与终端观测；终端默认展示 diff。
6. **Evaluation 一等公民**：用真实多步开发任务持续测量 agent 成功率。

## 2. 非目标

- 不兼容旧公开工具面（`change_prepare` / `change_apply` / 多 digest 仪式等）。
- 不提供迁移层、别名路由或双写兼容。
- 不把复杂恢复、journal、`expected_digest` 复制甩给模型。
- 不使用 Resource URI 做**变更审计**（宿主会触发下载，无法在上下文内审阅）。
- 不使用 `handoff_token`；多客户端直接复用 `remote_session_id`。
- 不做「自然语言描述修改意图、服务端自动匹配」的模糊编辑（本轮不做）。
- 不把读 + 写合成单一 MCP tool（保持 `readOnlyHint` 语义清晰）。

## 3. 背景与依据

### 3.1 问题

当前 MCPX 工具面与状态机过重：模型需精确复制 `remote_session_id`、`changeset_id`、`expected_digest`、`confirmation_token`、多种 digest 等，并遵守严格多步序列。自定义 patch / journal 路径在较大变更上开销明显。「model-friendly」宣传与实际 LLM 体验反差大。

### 3.2 决策

在 brainstorming 中确认：

| 决策 | 选择 |
| --- | --- |
| 优先级 | A（Agent 成功率与体验） |
| 方案 | 方案 2：干净核心重置 |
| 兼容 / 迁移 | 不做 |
| `remote_session_id` | **必填**（多客户端接力） |
| 接力 | 直接复用同一 ID，无 handoff_token |
| 变更审计 | 内联 `diff_summary`，不用 Resource URI |
| edit 实现 | 全新高效路径，不复用旧 changeset 引擎 |
| 变更行上限 | 单次调用 **变更总行 ≤ 1000**（严格 Unified Diff 统计） |

参考：mcp-builder / build-mcp-server 实践（annotations、actionable errors、Evaluation、Streamable HTTP）；高分 coding agent 的少工具 + 清晰错误导航。

## 4. 设计原则

1. **极简心理模型**：打开 session 后全程携带同一 `remote_session_id`。
2. **服务器承担复杂度**：格式保留、冲突检测、原子写入、幂等去重在服务端完成。
3. **幂等优先**：可重试操作推荐 `idempotency_key`。
4. **结构化错误**：`code` + `details` + `recovery` + `suggested_next`。
5. **标准 MCP 机制**：annotations、structuredContent、Resources（仅适合下载的大日志 / 产物）、Elicitation（确认优先）。
6. **Evaluation 驱动**：工具面与流程变更后必须跑相关 Evaluation。
7. **安全透明执行**：策略在服务端强制；模型看到的是清晰错误，而非策略语法课。

## 5. 公开工具面

### 5.1 工具清单

核心操作面控制在少量工具；session 为引导入口；扩展调用保持可发现。

| 工具 | 角色 | Annotation 要点 | 说明 |
| --- | --- | --- | --- |
| `session` | 会话生命周期 | Session | `open` / `close` / `attach`；**无 handoff_token** |
| `read` | 只读 | `readOnlyHint` | 文件 / 搜索 / 列表 / 上下文 / 环境；**支持批量** |
| `edit` | 变更 | `destructiveHint` + `idempotentHint` | 统一安全写入；**新实现**；批量 + 片段 |
| `execute` | 执行 | 视命令 | 命令或项目任务；短内联 / 长 `task_id` |
| `observe` | 观察 | `readOnlyHint` | `status` / `history` / `changes` / `logs` / `diff` |
| `plan` | 轻量计划 | 视 action | 结构化步骤 + evidence 引用 |
| `discover` | 发现 | `readOnlyHint` | Skill / 上游 MCP 列表与描述 |
| `skill_call` | 扩展执行 | 视策略 | 仅使用 discover 返回的名称 |
| `mcp_call` | 扩展执行 | 视策略 | 仅使用 discover 返回的 server/tool |
| `artifact` | 产物 | 视 action | 注册 / 读取测试报告、构建产物等 |

说明：相对旧 31 工具面大幅收敛；相对「理想 7 工具」保留 `session` 与扩展调用，以支撑必填 session 与扩展生态，仍远小于现状。

### 5.2 统一约定

- 有状态工具 **`remote_session_id` 必填**（`session` 的 `open` 除外：open 返回该 ID）。
- 重要调用携带 `purpose`。
- 可重试写操作推荐 `idempotency_key`。
- 结果：`content` 给人看的短摘要；机器字段在 `structuredContent`（或等价 `_meta`）。
- 大日志 / 产物可用 Resource；**变更 diff 必须内联**，禁止依赖 Resource 做审计。

## 6. 状态与 ID

### 6.1 硬约束

- `remote_session_id`：业务主键，**必填**；跨客户端直接复用同一值即可接力。
- 打开一次：`session(action=open, workspace, purpose)` → 保存完整 ID → 全程原样携带。
- 新客户端：只需持有同一 `remote_session_id`，无需 token。

### 6.2 最小化其他标识

| 标识 | 策略 |
| --- | --- |
| `idempotency_key` | 主重试键；服务端按 session + key 去重 |
| `base_sha256` | update 强烈推荐；来自 `read` |
| `task_id` / `plan_id` / `artifact_id` | 仅在对应流程从上一步返回复制 |
| `changeset_id` / `expected_digest` / `confirmation_token` | **不对模型暴露**（或确认仅用 `user_confirmed` / Elicitation） |

### 6.3 确认

- 优先 MCP Elicitation（宿主支持时）。
- 否则：`user_confirmed: true` + 同一业务参数重试。
- 不要求模型复制复杂 confirmation token。

## 7. edit 详细设计

### 7.1 目标

- 一次调用完成安全变更（默认 `apply: true`）。
- 支持只传片段、同文件多处不连续 replacement、跨文件批量。
- 严格行数上限；内联 diff；推送终端观测。
- **全新实现**：内存应用 + 原子写入；不走旧 journal 多阶段路径。

### 7.2 请求形态（示意）

```json
{
  "remote_session_id": "rs_xxx",
  "edits": [
    {
      "path": "src/App.vue",
      "operation": "update",
      "base_sha256": "sha256:...",
      "replacements": [
        { "match": "旧片段1", "replacement": "新片段1" },
        { "match": "旧片段2", "replacement": "新片段2" }
      ]
    },
    {
      "path": "src/utils.ts",
      "operation": "update",
      "base_sha256": "sha256:...",
      "replacements": [{ "match": "...", "replacement": "..." }]
    }
  ],
  "idempotency_key": "batch-edit-001",
  "purpose": "批量调整标题与工具函数",
  "apply": true
}
```

支持：

| operation | 主要字段 |
| --- | --- |
| `create` | `path` + `content` |
| `update` | `replacements[]`（推荐）或小文件 `content` 全量 |
| `delete` | `path`（+ 可选 base） |
| `rename` | `path` + `new_path` |

`replacements` 规则：

- 每条 `match` 必须在文件中**精确唯一**出现；多处或零处失败。
- 支持多行 match（含换行）。
- 同一文件多条不连续 replacement；服务端**从后往前**应用，避免偏移。
- 同一 `edits[]` 项对应一个 `path`；跨文件用多项。

### 7.3 变更总行（严格）

- 对每个文件生成 **Unified Diff**。
- **变更总行** = 所有 hunk 中 **删除行数 + 新增行数**（`+` / `-` 内容行；上下文行与 hunk 头不计）。
- 单次 `edit` 调用内**所有文件、所有 replacement 累加**。
- **合计 > 1000 → 整批拒绝**，错误码 `TOO_MANY_CHANGES`，返回实际统计。
- 统计必须来自真实 diff 算法，禁止用「新增行」近似。

### 7.4 响应（客户端）

```json
{
  "status": "succeeded",
  "summary": "已更新 2 个文件，变更总行 47",
  "diff_summary": "--- a/src/App.vue\n+++ b/src/App.vue\n@@ ...\n...",
  "total_changed_lines": 47,
  "results": [
    { "path": "src/App.vue", "new_sha256": "...", "changed_lines": 15 },
    { "path": "src/utils.ts", "new_sha256": "...", "changed_lines": 32 }
  ]
}
```

- `diff_summary`：内联文本，可直接展示给用户；控制长度（过长时按文件截断并注明）。
- **禁止**仅返回 Resource URI 作为变更审阅手段。

### 7.5 服务端实现要点（新路径）

1. 幂等：`(remote_session_id, idempotency_key)` 命中则返回上次结果（含相同 diff）。
2. 按 `base_sha256` 校验；不匹配 → `STALE_REVISION` + 当前 sha + `suggested_next`（重新 read 后同 key 重试）。
3. 读入内容 → 自后向前应用 replacements → 生成 per-file 与合计 diff 与行数。
4. 行数超限则不写盘。
5. 格式（charset、BOM、line_ending、final_newline）在首次读时记录并在写出时保留。
6. 原子写：临时文件 + `rename` + 必要 fsync。
7. 失败：不留下半写入；整体失败（本路径不做旧式 multi-ordinal journal）。
8. 策略 / ACL 检查失败 → `POLICY_DENIED` 等结构化错误。
9. 成功后写入观察事件（含 `diff_summary`、`total_changed_lines`、路径）。

### 7.6 与 read 的配合

`read` 支持批量：

```json
{
  "remote_session_id": "rs_xxx",
  "items": [
    { "path": "src/App.vue", "mode": "full" },
    { "path": "src/utils.ts", "mode": "window", "offset": 1, "limit": 80 }
  ],
  "max_total_bytes": 1048576
}
```

返回各文件 content + `sha256` + 格式元数据，供 edit 使用。

## 8. execute

- 统一入口：`command` 或项目 `task` 二选一。
- 短命令：等待完成，内联 stdout/stderr 摘要 + `exit_code`。
- 超时 / 长任务：返回 `task_id`；后续 `observe(view=status|logs)`。
- `idempotency_key`、`purpose`、策略 confirm 同 edit 原则。
- 输出推送终端观测；模型用 observe 拉结构化日志。
- 安全：`security.commands` 仍生效。

## 9. observe 与终端观测

### 9.1 视图

| view | 用途 |
| --- | --- |
| `status` | 轻量状态 |
| `history` | 广义历史 |
| `changes` | **最近变更列表 + `diff_summary`**（专用审计视图） |
| `diff` | 指定变更深入 |
| `logs` | 任务输出（分页 / 偏移） |

`view=changes` 返回示例字段：`id`、`path`、`operation`、`total_changed_lines`、`diff_summary`、`timestamp`、可选 `next_sequence`。

### 9.2 终端观测（`mcpx-server observe`）

- **默认展示 diff**（路径 + 总行数 + 关键 diff 片段，可着色）。
- 仅当显式 `-diff=summary`（或等价）时降级为仅统计。
- 断线按 sequence 补偿，历史可含 diff。
- 批量 edit 可聚合为一条观测事件，diff 按文件分组。
- 文本默认使用紧凑行式 CLI 输出，内部 operation 调度和重复远端 notice 静默；
  `-format json` 保留完整结构化事件。

### 9.3 事件

- edit / execute / plan 关键完成事件进入 broker + store。
- 对模型：工具返回已含摘要；observe 用于回顾与增量。
- 对人类：终端默认可读 diff，减少「不知道模型改了什么」。

## 10. plan

轻量结构化工作流，非重型项目管理：

- action：`create` / `advance` / `complete` / `block` / `replan`
- tasks：`id`、`title`、`status`、`evidence`
- evidence：引用工具返回（edit 摘要、task_id、exit_code、artifact_id 等），不复制大段正文
- 不强制每任务使用；跨多步、需向用户交代进度时使用

## 11. discover / skill_call / mcp_call / artifact

- **必须先** `discover` 再调用；禁止凭记忆猜测 Skill / MCP 名称。
- `skill_call` / `mcp_call`：名称来自 discover；走同一 `remote_session_id` 与策略。
- `artifact`：register / read；可与 plan evidence 联动。
- 扩展调用结果进入观察通道。

## 12. 错误模型

统一结构：

```text
error.code
error.message
error.details          // path、index、current_sha256 等
error.recovery         // 自然语言
error.suggested_next   // 推荐调用模板（含须原样复制的 remote_session_id / idempotency_key）
```

常见 code（示例）：

| code | 含义 |
| --- | --- |
| `STALE_REVISION` | base_sha 不匹配 |
| `MATCH_NOT_FOUND` / `MATCH_AMBIGUOUS` | replacement 失败 |
| `TOO_MANY_CHANGES` | 变更总行 > 1000 |
| `POLICY_DENIED` | 策略拒绝 |
| `SESSION_REQUIRED` / `SESSION_NOT_FOUND` | session 问题 |
| `IDEMPOTENCY_CONFLICT` | 幂等冲突（若语义需要） |

## 13. 推荐工作流

```text
session open → 保存 remote_session_id
read（可批量）→ sha256 + 片段
edit（可批量，replacements，idempotency_key）→ diff_summary
execute → 验证
observe(view=changes|logs) → 确认
plan advance（evidence）→ 记录
（可选）discover / skill_call|mcp_call / artifact
```

多客户端：B 客户端直接使用 A 给出的同一 `remote_session_id` 继续。

## 14. Evaluation

Evaluation 为生产级必交付物，覆盖：

1. 单步：read → edit → execute
2. 恢复：STALE / POLICY / MATCH 失败后按 `suggested_next` 恢复
3. 跨客户端：同一 `remote_session_id` 接力完成剩余步骤
4. 批量：多文件 edit + 行数上限拒绝
5. 观察：`view=changes` 与终端 diff 一致性（逻辑级）
6. 计划：evidence 正确引用 edit / execute 结果

示例成功标准（A 视角，可在实现计划中量化）：

- 基础任务成功率高
- 跨客户端接力可用
- 平均轮次相对旧面显著下降
- 低级错误（猜 ID、重复 open、错 digest）接近消除

## 15. 安全与生产边界

- 命令 / 文件策略保留；违规结构化返回。
- `remote_session_id` 绑定角色与审计上下文。
- 审计日志服务端持久化；模型侧不依赖 Resource 审计 diff。
- 鉴权：本地 bearer/open；远程 OAuth / dual 等既有能力可保留（实现阶段对齐）。
- 输出脱敏：密钥类不进 diff_summary / 日志摘要。

## 16. 部署与演进

- 形态：本地长期守护进程；`--workspace` 注册项目；Streamable HTTP `/mcp`。
- 远程：反向代理 HTTPS + 鉴权；客户端配置同一 Runtime。
- 配置：`~/.mcpx/config.yaml` 为主；项目级仅描述与策略覆盖。
- 演进：允许干净 breaking；`runtime_read` 声明 revision 与推荐用法。
- 运维：使用 `mcpx-server observe <name>` 只读观测；`workspace` 仅负责注册路径。

## 17. 测试要点（实现阶段）

- edit：多 replacement、批量、行数边界（999 / 1000 / 1001）、STALE、唯一 match、格式保留、幂等。
- observe：`view=changes` 字段与 diff 内容；终端默认 diff 渲染。
- execute：短 / 长任务切换与 observe logs。
- session：跨「客户端」复用同一 ID 可见未完成状态。
- 回归：旧工具名不得再注册。

## 18. 实现分期建议（供 writing-plans）

1. **P0**：session + read（批量）+ edit（新引擎）+ observe（changes + 终端默认 diff）
2. **P1**：execute + 任务观测
3. **P2**：plan + artifact
4. **P3**：discover + skill_call + mcp_call
5. **P4**：Evaluation 套件 + runtime 能力声明 + 文档

每期以 Evaluation 子集为完成门槛。

## 19. 规格自检记录

| 检查项 | 结果 |
| --- | --- |
| 占位符 / TODO | 无未决「待定」；实现细节留给计划 |
| 内部一致 | `remote_session_id` 必填、无 handoff_token、无 Resource 审计 diff、新 edit 路径、1000 行严格统计一致 |
| 范围 | 单规格覆盖干净核心 Runtime；实现拆分见 §18 |
| 模糊性 | 行数定义、批量形态、终端默认 diff 已写死 |

---

**下一步**：用户审查本规格；确认后进入 `writing-plans` 编写分步实现计划。
