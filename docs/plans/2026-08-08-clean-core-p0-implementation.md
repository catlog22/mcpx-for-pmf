# 干净核心 P0 实现计划

> **面向 AI 代理的工作者：** 使用 executing-plans 或 subagent-driven-development 逐任务实现。步骤用 `- [ ]` 跟踪。默认 **实现 → 补测 → 跑测**（非 TDD）。**禁止**自动 commit/push，需用户确认。

**目标：** 落地规格 `docs/specs/2026-08-08-production-mcp-runtime-clean-core-design.md` 的 **P0**：新 edit 引擎 + 收敛公开工具面中的 session / read / edit / observe（changes + 终端默认 diff）。

**架构：** 新增独立包 `internal/edit`（内存 multi-replace + 严格 Unified Diff 行统计 + 原子写），不依赖 `internal/changeset` 应用路径。`internal/server` 注册干净核心工具名，旧工具名从 `tools/list` 移除。观察层默认 full diff。

**技术栈：** Go 1.26+、标准库 testing、现有 `internal/file` 路径与格式、`internal/observation` 渲染。

**规格：** `docs/specs/2026-08-08-production-mcp-runtime-clean-core-design.md` §5–§9、§18 P0。

## 实现进度（2026-08-08）

- `internal/edit` 已完成：精确唯一 replacement、重叠校验、base SHA、create/update/delete/rename、严格 Unified Diff 统计、999/1000/1001 边界、批量超限整批拒绝、BOM/UTF-8/UTF-16/混合换行/末尾换行保留、原子写入。
- clean-core 公开面已切换为 `session`、`read`、`edit`、`observe`；旧 P0 工具名不出现在 `tools/list`，`session(attach)` 直接复用 `remote_session_id`，不再暴露 handoff 流程。
- `read` 已支持 `items[]` 的 full/window 混合批量读取；`edit` 返回内联 `diff_summary`、累计变更行和结构化恢复字段；`observe(view=changes)` 返回最近 edit 变更；终端 workspace 观测默认 `-diff=full`。
- clean core 的交互补强已完成：大 diff 有界预览与分页、UTF-16 模型侧解码、带指纹和重启恢复的持久化幂等均已接入，详见 [P1–P4 总实现计划](2026-08-08-clean-core-p1-p4-implementation.md) 的「P0 交互补强」章节。
- 验证证据：`rtk go test ./... -count=1`（484 项通过）、`rtk go test -race ./... -count=1`（484 项通过）、`rtk go vet ./...`、`CGO_ENABLED=0 rtk go build -o bin/mcpx-server ./cmd/mcpx-server`、`test -z "$(gofmt -l ./cmd ./internal)"`、`rtk git diff --check` 均通过。
- 本轮未执行 commit/push，等待用户明确确认。

---

## 文件结构（P0）

| 路径 | 职责 |
| --- | --- |
| `internal/edit/types.go` | Edit 请求/结果/错误类型 |
| `internal/edit/apply.go` | 批量 apply、replacements 从后往前、幂等键外的核心逻辑 |
| `internal/edit/diff.go` | Unified Diff 生成与严格变更行统计 |
| `internal/edit/write.go` | 原子写入与格式写出 |
| `internal/edit/apply_test.go` | 引擎单测 |
| `internal/server/tools_clean_core.go` | 干净核心工具注册与 handler 入口 |
| `internal/server/tools_edit.go` | edit 工具：鉴权、session、策略、调用 edit 包、观测事件 |
| `internal/server/tools_read.go` | read 批量（可适配现有 source） |
| `internal/server/tools_observe.go` | observe 含 view=changes |
| `internal/server/tools_catalog.go` | 切换为干净核心注册 |
| `internal/observation/*` | 终端默认 diff（已有 full 默认则对齐文档） |
| `internal/server/guidance/agent.yaml` | 对齐干净核心指导 |

---

### 任务 1：`internal/edit` 引擎核心

**文件：**
- 创建：`internal/edit/types.go`
- 创建：`internal/edit/diff.go`
- 创建：`internal/edit/write.go`
- 创建：`internal/edit/apply.go`
- 测试：`internal/edit/apply_test.go`

- [x] **步骤 1：定义类型与错误**

`Replacement`、`FileEdit`（path/operation/base_sha256/content/replacements/new_path）、`BatchRequest`、`BatchResult`、`FileResult`、错误：`ErrStale`、`ErrMatchNotFound`、`ErrMatchAmbiguous`、`ErrTooManyChanges`、`MaxChangedLines = 1000`。

- [x] **步骤 2：实现 Unified Diff 与严格行统计**

对 old/new 文本生成 unified diff；`ChangedLines(diff) = 所有以 + 或 - 开头且非 +++ / --- 的行数`（不含 `@@`、上下文空格行）。

- [x] **步骤 3：实现 apply 单文件**

- update + replacements：从后往前；每条 match 必须恰好 1 次；统计合计行数。
- update + content：全量替换，按 diff 计行。
- create / delete / rename。
- base_sha256 校验。
- 超 1000 行返回 `ErrTooManyChanges` 且不写盘。

- [x] **步骤 4：原子写 + 格式**

使用 `file.DetectFormat`；写出时保留 line ending；临时文件 + rename。

- [x] **步骤 5：单测并跑通**

```bash
rtk go test ./internal/edit -count=1
```

覆盖：多 replacement、唯一/歧义 match、STALE、999/1000/1001 行边界、create/delete、批量两文件合计超限。

- [x] **步骤 6：展示变更摘要，经用户确认后再 commit**（本轮已展示摘要，未自动 commit）

---

### 任务 2：edit 工具接入 Runtime

**文件：**
- 创建：`internal/server/tools_edit.go`
- 修改：`internal/server/tools_catalog.go`（或新建 clean 注册并切换 `registerTools`）
- 测试：`internal/server/tools_edit_test.go`

- [x] 解析 `edits[]` / `replacements[]` / `idempotency_key` / `remote_session_id`
- [x] 解析 workspace、策略、session 校验
- [x] 调用 `edit.ApplyBatch`
- [x] 返回 `diff_summary`、`total_changed_lines`、结构化 error
- [x] 成功时写入 observation 事件（含 diff_summary）
- [x] `rtk go test ./internal/server -run Edit -count=1`

---

### 任务 3：read 批量 + session 收敛

**文件：**
- 修改或新建 read/session handlers
- 公开工具名：`read`、`session`（action open/close/attach，无 handoff_token）

- [x] `read` 支持 `items[]` 批量
- [x] `session` open 返回必填复用的 `remote_session_id`
- [x] 从 tools/list 移除旧名或不再注册（干净 breaking）
- [x] 相关单测

---

### 任务 4：observe view=changes + 终端默认 diff

**文件：**
- `internal/server/tools_observe.go`（或等价）
- `cmd/mcpx-server` workspace 观测默认 diff
- `internal/observation` 默认 DiffModeFull（确认 CLI 默认）

- [x] `observe(view=changes)` 返回变更列表 + diff_summary
- [x] 终端观测默认展示 diff；仅 `-diff=summary` 降级
- [x] 单测 / 观测集成测

---

### 任务 5：P0 验收

- [x] `rtk go test ./internal/edit ./internal/server ./internal/observation -count=1`
- [x] `rtk go test ./... -count=1`（修复因工具面 breaking 导致的 catalog 测试）
- [x] 更新 `agent.yaml` / prompts 与干净核心一致
- [x] 对照规格 §17 P0 相关项勾选

---

## P1–P4（总计划已敲定）

详细实现、最终公开工具边界、阶段依赖和验收门槛见：[P1–P4 总实现计划](2026-08-08-clean-core-p1-p4-implementation.md)。

| 期 | 内容 |
| --- | --- |
| P1 | `execute` + Task 观测，移除 `command_run` / `task_read` / `task` |
| P2 | `plan` + `artifact` + Evidence，移除 `plan_read` / `artifact_read` |
| P3 | `discover` + `skill_call` + `mcp_call`，移除 `extension_discover` |
| P4 | Evaluation + Runtime 能力声明 + README + 最终 catalog 收口 |

P1–P4 已统一立项并完成 P1 → P2 → P3 → P4 顺序实现；最终场景矩阵与命令见 [Clean Core P1–P4 Evaluation](../evaluations/clean-core-p1-p4.md)。

## 执行说明

- 当前分支可能已有 observation 未提交改动：保留并叠加，勿 `reset --hard`。
- 不自动 commit；每完成任务可向用户提议 commit message。
- 后端：实现后写测并 `go test` 相关包。
