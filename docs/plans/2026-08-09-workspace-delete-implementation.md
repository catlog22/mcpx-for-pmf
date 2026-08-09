# MCPX 受限 Workspace 文件删除实现计划

> **面向 AI 代理的工作者：** 本计划在当前版本直接切换生产删除契约，不保留旧版 `edit(operation=delete)` 提交路径。

**目标：** 将文件/显式目录删除重构为网页端模型询问用户、服务端 confirmation UUID 可验证、manifest 冻结、可幂等重放并可审计的 `remove_prepare` / `submit_remove` 两阶段能力，同时修复 `edit(apply=false)` 的误写盘问题。

**架构：** prepare 只在 registered Workspace 内解析显式 file/directory targets；目录会冻结完整树、文件 SHA、目录数量和总字节数后持久化 manifest，并返回服务端生成的 confirmation UUID。网页端模型向用户展示冻结清单并询问；submit 只接受 delete request、manifest SHA、confirmation UUID 和幂等键，重新校验 manifest 与当前树后由服务端分块删除。

**技术栈：** Go 1.26.1、MCP Go SDK、SQLite migration、`os.Root`、现有 ARC/审计/Remote Session/幂等设施。

---

### 任务 1：锁定误写盘与旧 delete 入口行为

**文件：**
- 修改：`internal/edit/types.go`、`internal/edit/apply.go`
- 修改：`internal/server/tools_edit.go`、`internal/server/tools_clean_core.go`
- 测试：`internal/server/tools_edit_test.go`

- [x] 增加 `BatchRequest.DryRun`，dry-run 在构造完整 BatchResult 后直接返回，不能调用 beforeWrite、原子写、Remove、rename、observe 或 file.changed。
- [x] `toolEdit` 读取 `apply`；`apply=false` 走 DryRun 并返回 `applied=false`，不创建 edit record、不写幂等终态、不发文件变更事件。
- [x] 从公开 edit schema 移除 `delete` enum；runtime 直接拒绝手工提交 `edit(operation=delete)`，恢复动作指向 `remove_prepare`。
- [x] 已运行 `go test ./internal/server -run 'TestRemove|TestCleanEditApplyFalseNeverMutatesFilesystem' -count=1`，dry-run 与旧 delete 入口回归通过。

### 任务 2：新增持久化 delete request manifest

**文件：**
- 修改：`internal/state/migrations.go`
- 创建：`internal/deletion/service.go`
- 测试：`internal/deletion/service_test.go`

- [x] 增加 `delete_requests` 表，保存 request ID、session/principal、workspace、purpose、不可变 manifest JSON/SHA、状态、expiry、confirmation UUID hash、commit result JSON 和时间戳。
- [x] 实现 Create/Get/FindByIdempotency/BindApproval/MarkCommitting/Complete，使用状态条件更新避免并发 commit 重复执行；已完成请求按 request ID + manifest SHA + idempotency key 返回原结果。
- [x] receipt 只保存 SHA-256，不持久化原始 receipt；manifest 只保存路径、SHA、size，不保存文件内容。

### 任务 3：实现 `remove_prepare`

**文件：**
- 创建：`internal/server/tools_workspace_delete.go`（内部实现文件名；公开工具为 `remove_prepare`）
- 修改：`internal/server/tools_clean_core.go`、`internal/server/tools_catalog.go`、`internal/server/capabilities.go`
- 修改：`internal/server/prompts/tools.yaml`、`internal/server/guidance/agent.yaml`
- 测试：`internal/server/tools_remove_test.go`

- [x] 暴露严格 schema：`remote_session_id`、`workspace`、`purpose`、`targets[]`、`idempotency_key`；target 只允许 `path`、`kind=file|directory`、`expected_sha256`，拒绝 glob 和 shell 参数。
- [x] 对每个 path 拒绝 absolute、`.`/`..` 非规范路径、workspace 外路径、symlink、special file；目录只接受显式指定并冻结完整目录树。
- [x] 校验 regular file 当前 SHA；directory 计算树摘要，计算 size/count/total bytes，按 canonical path 排序后计算 manifest SHA。
- [x] prepare 不删除、不产生 filesystem `file.changed`，只写 manifest/audit `prepared` 事件；响应包含 request ID、confirmation UUID、manifest SHA、targets、entries、expiry、requires_user_confirmation。
- [x] 标记 `ReadOnlyHint=true`、`DestructiveHint=false`、`IdempotentHint=true`、`OpenWorldHint=false`，并发布 `filesystem_only`、`registered_workspace` 等 `_meta`。

### 任务 4：实现 `submit_remove` 与网页确认 UUID

**文件：**
- 修改：`internal/server/tools_workspace_delete.go`
- 修改：`internal/server/observability.go`、`internal/audit/audit.go`（仅在现有字段不足时）
- 测试：`internal/server/tools_remove_test.go`

- [x] commit schema 只接受 request ID、manifest SHA、confirmation UUID、idempotency key 和语义上下文，不接受 targets/glob/shell。
- [x] confirmation UUID 由 prepare 服务端生成并绑定 request ID、manifest、session、workspace、expiry；缺失返回 `CONFIRMATION_REQUIRED`，不匹配返回 `CONFIRMATION_MISMATCH`。
- [x] 重新扫描冻结 manifest、校验 session/workspace/principal/expiry/manifest SHA、每个当前 SHA、目录树、symlink/path policy；确认后发生变化时不产生删除。
- [x] commit 使用 `os.Root.Remove`，服务端内部按 64 项 bounded chunk 执行；每个 entry 返回 deleted/failed，最终状态明确为 committed/partial/failed。
- [x] 幂等 replay 返回原结果；同 key 不同 manifest 返回 `IDEMPOTENCY_CONFLICT`；并发 commit 只能一个 owner，另一个返回可重试状态。
- [x] 记录 prepare、confirmation UUID hash、commit、每个 entry 的前置 SHA、结果、event ID、session/principal/purpose；不记录确认原文和 secret。
- [x] 标记 `ReadOnlyHint=false`、`DestructiveHint=true`、`IdempotentHint=true`、`OpenWorldHint=false`，标题和描述明确不执行命令、不接受自由目标、只提交用户已确认的 manifest。

### 任务 5：专项边界与回归验证

**文件：**
- 修改：`internal/server/public_catalog_test.go`、`internal/server/catalog_regression_test.go`
- 测试：`internal/server/tools_remove_test.go`、`internal/server/tools_edit_test.go`

- [x] 覆盖 prepare non-destructive、apply=false、无 approval、确认后 SHA 变化、exact replay、traversal、symlink、显式 directory tree。
- [ ] 仍需补齐 expiry、receipt/manifest mismatch、special file、1000+ entries、并发 commit 和 approval audit 的专项矩阵。
- [ ] 覆盖 1/20/100/1000+ targets、部分失败、并发 commit、duplicate commit、durable audit 和 tools/list/runtime_read safety metadata。
- [x] 已运行全量 `go test ./... -count=1`、聚焦 race 回归、`go vet ./...`、`gofmt`、`git diff --check` 和 `CGO_ENABLED=0 go build -o /tmp/mcpx-server-delete-verify ./cmd/mcpx-server`。
