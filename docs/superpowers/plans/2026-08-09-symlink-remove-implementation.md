# 符号链接删除实现计划

> **面向 AI 代理的工作者：** 在当前工作区内按步骤执行；不兼容旧删除接口，不提交代码。

**目标：** 允许 `remove_prepare`/`submit_remove` 删除 Workspace 内的符号链接目录项和整棵目录树，同时严格禁止跟随或修改链接目标。

**架构：** 删除清单使用 `lstat` 冻结最终目录项类型；符号链接记录链接文本及其 SHA-256，而不是目标内容。目录目标只冻结根路径，不扫描或返回子树，并把根路径作为已授权删除范围。提交时再次 `lstat`：symlink 直接 unlink，directory 使用受 Workspace Root 约束的 `RemoveAll` 删除当前树；目录内容变化与 directory→symlink 均不跟随目标也不阻断提交。

**技术栈：** Go 1.26.1、`os.Root`、标准库 `os`/`filepath`/`crypto/sha256`、标准库 `testing`。

---

### 任务 1：核对删除数据模型并补充符号链接清单字段

**文件：**
- 修改：`internal/deletion/service.go`
- 修改：`internal/server/tools_workspace_delete.go`
- 测试：`internal/server/tools_remove_test.go`

- [ ] 为删除目标增加符号链接的稳定属性：`kind`、链接文本和链接文本 SHA-256；保持普通文件/目录现有字段序列化稳定。
- [ ] 明确符号链接的大小使用链接文本字节数，不读取目标内容。
- [ ] 保持 manifest SHA 将这些字段纳入 canonical payload，避免链接被替换后继续复用旧 manifest。

### 任务 2：放开最终符号链接并保持路径边界

**文件：**
- 修改：`internal/server/tools_workspace_delete.go`
- 修改：`internal/server/tools_clean_core.go`
- 修改：`internal/server/limits.go`

- [ ] 显式目标仅允许 Workspace 内最终节点为 symlink；绝对路径、`..` 越界、Workspace 外父目录和中间 symlink 继续拒绝。
- [ ] 目录目标递归处理任意目录内容；不跟随 symlink，symlink 作为叶子；目录内特殊文件不阻断删除。
- [ ] `remove_prepare` 返回清晰的 symlink 类型、link text、link SHA、大小和“不跟随目标”安全元数据。

### 任务 3：提交阶段增加 symlink TOCTOU 校验

**文件：**
- 修改：`internal/server/tools_workspace_delete.go`
- 测试：`internal/server/tools_remove_test.go`

- [ ] 提交前验证最终节点：symlink 直接删除链接入口；directory 删除当前树；explicit file 仍执行 SHA 校验。directory→symlink 允许，directory→regular file 返回 `STALE_REVISION`。
- [ ] 使用 `os.Root.RemoveAll` 删除目录树，单独使用 `Remove` 删除 file/symlink 入口；不能使用 shell、跟随目标或递归删除 symlink 目标。
- [ ] 保持批量删除、幂等重放、并发提交和审计结果的现有契约。

### 任务 4：补充最小充分回归测试并验证

**文件：**
- 修改：`internal/server/tools_remove_test.go`
- 修改：`README.md`
- 修改：`docs/superpowers/specs/2026-08-09-symlink-remove-design.md`

- [ ] 覆盖 Workspace 内/外目标 symlink、目录内 symlink、目标缺失、最终 symlink 允许删除。
- [ ] 覆盖中间 symlink、绝对路径、`..` 拒绝，目录包含特殊文件仍可删除，directory→symlink 删除链接且不触碰目标，以及 directory→regular file 的无突变 stale。
- [ ] 运行 `gofmt`、专项 remove 测试、`go test ./... -count=1`、`go vet ./...`，确认现有未提交文件不被覆盖。
