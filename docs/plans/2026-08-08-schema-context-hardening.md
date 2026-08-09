# MCP 工具 Schema 与上下文边界修复计划

## 目标

修复严肃 Workspace 评估中阻塞 autonomous coding 的公开工具 Schema 问题，并收紧
`context(paths)` 的文件范围和大编辑错误恢复契约。

## 实现

1. `cleanActionTool` 将所有 `oneOf` 分支字段同步放入根层 `properties`；根层保持开放以兼容先检查对象属性、后评估 `oneOf` 的连接器，选中分支仍保留 `additionalProperties: false`。
2. 增加 union Schema 回归测试，并通过真实 Streamable HTTP `tools/call` 验证 `execute(run, command)` 与 `plan(create, goal, tasks)` 可以进入 handler。
3. `SmartQueryPage` 增加 `ScopePaths`，对 context seed 做目录后代/文件精确匹配，并与安全允许函数、glob 取交集。
   `SearchWith` 同样应用硬范围过滤，确保 `read(view=search, paths=...)` 不会泄漏 Workspace 根目录结果。
4. `TOO_MANY_CHANGES` 统一为 `validation`、可重试，并返回 `split_edit`、`max_changed_lines` 的结构化 recovery。
5. 增加 context/search scope、Schema、真实工具调用和错误恢复回归测试。

## 验证

- 定向关键回归：6 passed
- `go test ./internal/edit -run 'TestTooManyChanges' -count=1`：5 passed
- `go test ./... -count=1`：491 passed
- `go vet ./...`
- `gofmt -l ./cmd ./internal`
- `git diff --check`
- CLI 构建通过

## 状态

- 2026-08-08：已完成实现与验证。
- 目标包 race：`go test -race ./internal/envelope ./internal/source ./internal/server -count=1`，163 passed。
