# MCPX for PMF 增量说明

本目录记录 "mcpx for pmf" fork 相对上游 [opentokenz/mcpx](https://github.com/opentokenz/mcpx) 的增量：

| 增量 | 位置 | 说明 |
| --- | --- | --- |
| `pi_execute` 工具 | `internal/server/tools_pi_execute.go` | 将 plan 任务委派给本地 Pi Agent（`pi -p --no-session --approve`，无 shell 解析），支持 system 提示注入，Plan 证据闭环（成功 complete / 失败 block） |
| Pi 插件 skill 识别 | 全局配置 `discovery.skills.extra_dirs` | 扫描 `pi-maestro-flow/.pi/skills`，经 `skill_tool list/describe` 暴露 |
| 工作区自动注册 | pi-maestro-flow 插件侧 | 插件启动时自动 `mcpx workspace register` 当前项目 |

## 快速开始

```bash
# 1. 构建
go build -o bin/mcpx ./cmd/mcpx-server

# 2. 启动（注册工作区）
./bin/mcpx --workspace /path/to/project
# 或运行中注册：mcpx workspace register /path/to/project

# 3. Pi skill 识别（~/.mcpx/config.yaml）
# discovery:
#   skills:
#     extra_dirs:
#       - D:/pi-maestro-flow/.pi/skills

# 4. pi_execute 白名单（默认 allow 策略下无需配置；如需收紧）
# security:
#   commands:
#     allow:
#       - "^pi(\\s|$)"
```

## 远程 Web 规划 → 本地 Pi Agent 闭环

1. 远程客户端（ChatGPT/Claude 等）经 Streamable HTTP 连接 mcpx，`session open` 绑定工作区；
2. `plan create`（goal + tasks）→ 远程 Web 侧规划；
3. `pi_execute`（`plan_id` + `plan_task_id` + `prompt`，可选 `system` 注入）→ mcpx 无头启动本地 Pi Agent 执行；
4. 进程在任务系统持久化（可 `execute attach/stop` 延续）；结束后自动 `plan complete_task`（exit 0）或 `block_task`（失败），证据为 execute 类型；
5. 远程客户端 `plan read` 查看任务状态与证据，继续推进后续任务。
