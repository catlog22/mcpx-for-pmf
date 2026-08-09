# MCPX CLI 命令语义拆分计划

## 目标

让“注册/管理工作区”和“终端观测运行事件”在命令行上形成清晰、可迁移的两个入口，降低模型和人使用时的歧义。

## 方案

1. 将 `mcpx-server observe <workspace>` 作为终端观测的规范入口，观测参数保持不变。
2. 增加 `mcpx-server workspace register <path>` 作为注册/更新工作区的明确入口；注册成功后只修改全局配置，不启动 Runtime。
3. 移除顶层 `mcpx-server --workspace <path>` 注册快捷方式；注册必须先执行 `workspace register`，再启动服务。
4. 移除 `mcpx-server workspace [观测参数] <name>` 旧观测别名，不保留 CLI 兼容分支。
5. 更新 README、CLI 帮助和少量解析测试，验证规范入口与非法旧入口的边界。

## 验证

- `go test ./cmd/mcpx-server -count=1`
- `go test ./... -count=1`
- `gofmt -l ./cmd ./internal`
- `go vet ./...`

## 状态

- 2026-08-08：按需求改为非兼容切换，已完成。
- 规范观测入口：`observe [flags] <name>`。
- 规范注册入口：`workspace register <path>`，验证为只写全局配置后退出。
- 非兼容要求：旧 `workspace <name>` 和 `--workspace <path>` 均移除。
- Runtime 只加载已注册的全局配置，不再通过启动参数隐式注册工作区。
- 验证通过：定向测试 174 passed、全量测试 486 passed、`go vet ./...`、`gofmt -l ./cmd ./internal`、`git diff --check`、CLI 构建和入口行为检查。
