# Changeset Digest 语义引导实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans` 逐任务实现此计划。步骤使用复选框（`- [ ]`）跟踪进度。

**目标：** 让模型在准备、读取和应用 Changeset 时明确识别并原样复用 `digest`，并在错误后获得可直接执行的恢复参数。

**架构：** 保持 Changeset 的单一 digest 来源不变；在结构化响应中增加语义别名和下一动作，在输入 schema 与错误响应中明确禁止使用 diff 统计、快照 ID 或空值。历史查询同时支持领域事件类型与观测事件类型的匹配。

**技术栈：** Go、标准库 `encoding/json`、SQLite observation store、现有 MCPX Envelope 与 Recovery 结构。

---

## 文件清单

- 修改：`internal/server/tools_changeset.go`：输出 `expected_digest` 和可复用的 apply 恢复动作。
- 修改：`internal/server/tools_change_execute.go`：digest 不匹配时返回实际 digest 与重试参数。
- 修改：`internal/server/tools_catalog.go`：明确 `expected_digest` 的原样复制语义。
- 修改：`internal/observation/history.go`：让 `kinds` 支持 `changeset.prepared` 等领域事件别名。
- 测试：`internal/server/tools_change_recovery_test.go`、`internal/server/acceptance_protocol_test.go`、`internal/observation/history_test.go`。

不提交 `docs/`，不自动创建实现 commit。

## 任务 1：锁定 digest 输出契约

**文件：**
- 修改：`internal/server/tools_changeset.go`
- 修改：`internal/server/tools_catalog.go`
- 测试：`internal/server/acceptance_protocol_test.go`

- [x] **步骤 1：增加失败测试**

验证 `change_read(view=diff)` 与 `change_prepare(apply=false)` 的结构化数据同时包含：

```json
{
  "digest": "sha256:...",
  "expected_digest": "sha256:...",
  "next_action": {
    "tool": "change_apply",
    "arguments": {
      "changeset_id": "chg_...",
      "expected_digest": "sha256:..."
    }
  }
}
```

断言 `digest == expected_digest`，且下一动作不使用 diff 统计或快照 ID。

- [x] **步骤 2：运行失败测试**

运行：

```bash
rtk go test ./internal/server -run 'TestChange.*Digest|TestAcceptance.*Digest' -count=1
```

预期：因 `expected_digest` 和 `next_action` 尚未输出而失败。

- [x] **步骤 3：实现结构化输出**

在 `changeSummaryDTO` 中保留 `digest`，新增：

```go
dto["expected_digest"] = item.Digest
dto["next_action"] = map[string]any{
    "tool": "change_apply",
    "arguments": map[string]any{
        "changeset_id": item.ID,
        "expected_digest": item.Digest,
    },
}
```

只对可应用的 draft Changeset 输出 `change_apply` 下一动作；已应用或回滚状态不得误导模型继续 apply。

- [x] **步骤 4：修改 schema 文案**

将 `change_execute`、`change_apply` 的 `expected_digest` 描述改为明确语义：必须逐字复制服务端返回的 `digest` / `expected_digest`；`+211 −0`、`tree_digest`、snapshot ID 和空字符串都无效。

- [x] **步骤 5：运行测试确认通过**

运行：

```bash
rtk go test ./internal/server -run 'TestChange.*Digest|TestAcceptance.*Digest' -count=1
```

预期：所有新增和既有 Changeset 契约测试通过。

## 任务 2：让 digest 冲突可自恢复

**文件：**
- 修改：`internal/server/tools_change_execute.go`
- 修改：`internal/server/tools_changeset.go`
- 测试：`internal/server/tools_change_recovery_test.go`

- [x] **步骤 1：增加错误恢复失败测试**

使用存在的 draft Changeset 传入错误值 `+211 −0`，断言失败响应包含：

```json
{
  "error": {
    "code": "PATCH_CONFLICT",
    "details": {
      "changeset_id": "chg_...",
      "expected_digest": "sha256:..."
    },
    "recovery": {
      "tool": "change_apply",
      "arguments": {
        "changeset_id": "chg_...",
        "expected_digest": "sha256:..."
      }
    }
  }
}
```

- [x] **步骤 2：运行测试确认失败**

运行：

```bash
rtk go test ./internal/server -run 'Test.*Digest.*Recovery' -count=1
```

预期：失败响应没有正确 digest 或可执行 recovery 参数。

- [x] **步骤 3：实现带上下文的 digest 错误**

在 `executePreparedChange` 获取 Changeset 后，对 digest 不匹配使用专用错误响应，复用 `item.ID` 和 `item.Digest` 填充 `ErrorBody.Details` 与 `Recovery.Arguments`。不得把 digest 当作认证信息，也不得改变 Changeset 状态或工作区文件。

- [x] **步骤 4：运行测试确认通过**

运行：

```bash
rtk go test ./internal/server -run 'Test.*Digest.*Recovery' -count=1
```

预期：错误 digest 可被模型按 recovery 原样重试，且文件仍未被错误请求修改。

## 任务 3：修正历史查询领域事件别名

**文件：**
- 修改：`internal/observation/history.go`
- 测试：`internal/observation/history_test.go`

- [x] **步骤 1：增加失败测试**

写入一个 `observer.notice` 事件，输出 metadata 的 `source_type` 为 `changeset.prepared`，再用 `Kinds: []string{"changeset.prepared"}` 查询；同时写入一个实际 `event_type=changeset.prepared` 的事件，断言两者都可按该 kind 找到。

- [x] **步骤 2：运行测试确认失败**

运行：

```bash
rtk go test ./internal/observation -run 'TestHistory.*Changeset|TestHistory.*Kind' -count=1
```

预期：当前实现只按 `event_type` 比较，返回结果不完整。

- [x] **步骤 3：实现别名过滤**

对 `changeset.prepared`、`changeset.applied`、`changeset.reverted` 等领域事件，在 SQL 中匹配：

```sql
event_type = ? OR (event_type = 'observer.notice' AND output_json LIKE ?)
```

保持现有 `tool`、`command`、`error` 等类别行为不变，并继续使用参数化 SQL。

- [x] **步骤 4：运行测试确认通过**

运行：

```bash
rtk go test ./internal/observation -run 'TestHistory.*Changeset|TestHistory.*Kind' -count=1
```

预期：领域事件和观测事件均可按 kind 查询。

## 任务 4：回归验证

- [x] **步骤 1：运行相关包测试**

```bash
rtk go test ./internal/server ./internal/observation -count=1
```

- [x] **步骤 2：运行全量验证**

```bash
rtk go test ./... -count=1
rtk go test -race ./... -count=1
rtk go vet ./...
test -z "$(gofmt -l ./cmd ./internal)"
rtk git diff --check
```

- [x] **步骤 3：检查提交边界**

确认 `docs/superpowers/` 仍被 `.gitignore` 忽略，代码实现保持未提交，既有用户文件 `AGENTS.example.md` 不被修改。
