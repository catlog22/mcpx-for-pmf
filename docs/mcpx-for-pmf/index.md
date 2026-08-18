# MCPX for PMF 增量说明

本目录记录 "mcpx for pmf" fork 相对上游 [opentokenz/mcpx](https://github.com/opentokenz/mcpx) 的增量：

| 增量 | 位置 | 说明 |
| --- | --- | --- |
| `pi_window` 工具 | `internal/server/tools_pi_window.go` | 发现运行中的 Pi 窗口并消息投递（workspace-peer 文件协议，与 teammate-send 同通道）：`list` 列出自动注册的活跃窗口，`send` 先确认后投递（steer/follow_up），可关联 plan 任务 |
| `pi_execute` 工具 | `internal/server/tools_pi_execute.go` | 回退通道：无窗口在线时 spawn `pi -p` 执行（companion 式 + system 注入 + plan 证据闭环） |
| Pi 插件 skill 识别 | 全局配置 `discovery.skills.extra_dirs` | 扫描 `pi-maestro-flow/.pi/skills`，经 `skill_tool list/describe` 暴露 |
| 工作区自动注册 | pi-maestro-flow 插件侧 | 插件启动时自动 `mcpx workspace register` 当前项目 |

## 推荐流程（窗口委派，无需启动新进程）

```text
用户启动 pi（窗口自动注册：~/.pi/teammate/workspaces/{workspaceId}/runtime/owners/）
        │
前端（远程 Web 经 mcpx）
  1. pi_window list           → 展示可发现的 Pi 窗口（display_name / owner_id / 活跃度）
  2. 询问用户选择目标窗口
  3. pi_window send           → 服务端返回 USER_CONFIRMATION_REQUIRED（窗口 + 消息摘要）
  4. 用户确认后 user_confirmed=true 重试
  5. mcpx 写命令 mailbox → 目标窗口消费 → teammate-message 注入主会话 → 触发新一轮 turn
  6. 回执 accepted/queued → 前端可继续 plan 流程
```

## 快速开始

```bash
# 1. 构建
go build -o bin/mcpx ./cmd/mcpx-server

# 2. 启动（注册工作区）
./bin/mcpx --workspace /path/to/project

# 3. Pi 窗口发现前提：在该工作区启动任意 pi 窗口（pi-maestro-teammate 自动发布快照）

# 4. Pi skill 识别（~/.mcpx/config.yaml）
# discovery:
#   skills:
#     dirs:
#       - D:/pi-maestro-flow/.pi/skills
```

## 协议说明（对齐 pi-maestro-teammate workspace-peers v1）

- 窗口发现：`~/.pi/teammate/workspaces/{sha256(normalizedCwd)}/runtime/owners/{ownerId}.json`（20s 新鲜窗口）
- 命令投递：`.../commands/{toOwnerId}/{commandId}.json`（targetCorrelationId=`window-main-session`，messageKind=`request`，source=`system`，TTL 5 分钟）
- 回执：`.../responses/{fromOwnerId}/{commandId}.json`（accepted/rejected/expired/error）
- mcpx 身份：`~/.mcpx/pi-peer-identity.json`（fromOwnerId/fromOwnerNonce，幂等复用）
- 端到端脚本：`scripts/e2e-pi-window.mjs`（raw JSON-RPC，确认门控 + 投递验证）
