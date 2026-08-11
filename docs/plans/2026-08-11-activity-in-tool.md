# Activity-in-Tool 实现计划

> **面向 AI 代理的工作者：** 按任务顺序逐项实现此计划。步骤使用复选框跟踪；本次用户已批准设计，在当前会话内联执行，不自动 commit。

**目标：** 为会话内 MCP 工具增加聚合 Activity 输入，并由 Runtime 自动展开成真实 Activity V3 事件；旧 `/mcp/activity` HTTP ingress 不保留。

**架构：** schema 层提供统一 `activity` object；instrumentation 在业务 handler 前解析并持久化；现有 Activity store/renderer/ARC snapshot 继续消费单 kind event，不引入第二套持久化结构。

**技术栈：** Go、MCP Go SDK、SQLite observation/state store。

---

### 任务 1：锁定公共 schema

**文件：**
- 修改：`internal/server/tools_clean_core.go`
- 修改：`internal/server/tools_catalog.go`
- 修改：`internal/server/acceptance_protocol_test.go`
- 修改：`internal/server/public_catalog_test.go`

- [ ] 添加失败测试：核心/支持公共工具 schema 包含可选 `activity`，且仅允许六个字符串字段。
- [ ] 实现共享 `activityInputSchema()`，避免每个工具复制 schema。
- [ ] 确认 `activity` 不进入 required，也不接受 turn/sequence/state/related_call_id。
- [ ] 运行 server schema 相关测试。

### 任务 2：Runtime 自动展开 Activity

**文件：**
- 修改：`internal/server/agent_activity.go`
- 修改：`internal/server/observability.go`
- 修改：`internal/server/agent_activity_test.go`
- 修改：`internal/server/observability_regression_test.go`

- [ ] 添加失败测试：单次工具调用携带多个 Activity 字段时按 `intent,hypothesis,evidence,conclusion,next,status` 顺序记录。
- [ ] 添加失败测试：Runtime 自动生成 sequence/state/related_call_id，且业务 handler 在 Activity 接受失败时不运行。
- [ ] 提取共享 Activity 接受函数，使 HTTP ingress 和 tool ingress 复用同一持久化核心。
- [ ] 为 tool ingress 实现 runtime-managed turn/sequence；同一 session 的连续工具调用延续当前非终态 turn。
- [ ] 在 `instrumentTool` 记录 `tool.started` 前提交输入 Activity。
- [ ] 运行 Activity/observability 针对性测试。

### 任务 3：观察与 ARC 回归

**文件：**
- 修改：`internal/observation/timeline_test.go`
- 修改：`internal/server/presentation_regression_test.go`
- 视需要修改：`internal/observation/timeline.go`

- [ ] 添加测试：Activity 与工具同流时先展示 Activity，再展示工具；时间分割线继续存在。
- [ ] 添加测试：Intent 保持分割线样式，其余多个语义保持 `◇`。
- [ ] 添加测试：ARC 只读取已持久化 snapshot，不直接复制 raw `activity` input。
- [ ] 保证 full diff、command 输出、Read/Edit 展示现状不变。

### 任务 4：文档和验证

**文件：**
- 修改：`README.md`
- 修改：`internal/server/guidance/agent.yaml`
- 修改：`internal/server/prompts/tools.yaml`
- 修改：`internal/server/capabilities.go`

- [ ] guidance 改为优先在下一次业务工具调用的 `activity` 字段报告公开轨迹，不再要求客户端直接维护 Activity wire 字段。
- [ ] capabilities 说明 tool-embedded Activity 是 MCP clients 的入口。
- [ ] README 更新示例和语义边界。
- [ ] 运行 `gofmt`、针对性测试、`go test ./...`、核心 `go vet`、`go build ./...`、`git diff --check`。
- [ ] 审核最终 diff，确认时间分割线未删除、无 V1 兼容分支。
