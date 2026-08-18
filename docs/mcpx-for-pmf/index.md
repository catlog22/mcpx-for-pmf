# MCPX for PMF 增量说明

本目录记录 "mcpx for pmf" fork 相对上游 [opentokenz/mcpx](https://github.com/opentokenz/mcpx) 的增量：

| 增量 | 位置 | 说明 |
| --- | --- | --- |
| Maestro skill 包 | `examples/skills/maestro/` | 封装 `maestro search/load/knowledge` CLI，经 `skill_tool` 暴露 |
| `pi_execute` 工具 | `internal/server/tools_pi_execute.go` | 将 plan 任务下发本地 Pi Agent（`pi -p`），Plan 证据闭环 |
| 工作区自动注册 | pi-maestro-flow 插件侧 | 插件启动时自动 `mcpx workspace register` 当前项目 |

## 快速开始

```bash
# 1. 构建
go build -o bin/mcpx ./cmd/mcpx-server

# 2. 启动（注册工作区）
./bin/mcpx --workspace /path/to/project
# 或运行中注册：mcpx workspace register /path/to/project

# 3. Maestro skill（首次）
cp -r examples/skills/maestro ~/.mcpx/skills/maestro

# 4. pi_execute 白名单（~/.mcpx/config.yaml）
# security:
#   commands:
#     allow:
#       - "^pi(\\s|$)"
```

## 工具使用示例

- `skill_tool call` → `{"name": "maestro", "action": "search", "query": "..."}`
- `plan create` → `pi_execute`（`plan_task_id` + `title` + `description`）→ `plan read` 验证任务 completed + evidence。
