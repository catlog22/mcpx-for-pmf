# MCPX

MCPX 是运行在本地开发环境中的 MCP Runtime（网关）。它通过
Streamable HTTP 把本地 Workspace、源码、变更、命令、任务、环境和扩展能力
安全地提供给 ChatGPT、Claude、Cursor、Grok 等 MCP 客户端。

MCPX 的重点不是增加一个聊天界面，而是提供一套可审计、可恢复、对模型友好
的开发工具协议：客户端可以跨连接恢复同一个 Remote Session，模型可以用文件
版本、Changeset 摘要、Task ID 和能力版本避免重复读取和无效重试。

## 能力概览

| 能力 | 说明 |
| --- | --- |
| Remote Session | 持久化 Workspace 会话、角色权限、事件、接力和跨客户端恢复 |
| Workspace | 注册多个项目，并在创建会话时显式绑定项目根目录 |
| Source | 文件窗口、批量读取、搜索、文件列表和有界上下文；返回 SHA-256 与编码/换行元数据 |
| Changeset | 生成、审阅、应用、丢弃和回滚文件变更；检查版本、策略和语义确认 |
| Terminal | 执行命令或项目 Task；短命令内联返回，长命令持久化为 Task |
| Operation | 并行或有依赖地执行多个公开工具，并统一等待、分页、取消和恢复 |
| Project Task | 从项目配置中发现测试、构建和检查任务，并解析诊断信息 |
| Workspace State | 读取 Git 状态、快照、差异、监听结果和项目记忆 |
| Environment | 查看操作系统、架构、Shell、容器、资源、文件系统和工具链 |
| Extension | 发现并调用配置的 Skill 与上游 MCP Server |
| Artifact | 注册、列出和分页读取测试报告、构建产物、覆盖率和日志 |
| Screenshot | 截取显示器或屏幕区域，并通过 MCP ImageContent 返回 |
| Security | OAuth、Bearer、Remote Session ACL、命令/文件策略和语义确认 |
| Observation | 通过本机 Socket 观察工具调用、Task、Changeset 和操作事件 |

## 设计边界

MCPX 同时处理两类 Session：

| 标识 | 生命周期 | 用途 |
| --- | --- | --- |
| `Mcp-Session-Id` | Streamable HTTP 传输层临时标识 | 连接和协议状态，重连或换客户端后可能变化 |
| `remote_session_id` | SQLite 持久化业务标识 | Workspace、角色、Changeset、Task、操作、快照和产物的主键 |

客户端应始终原样保存并复用服务端返回的 `remote_session_id`、`changeset_id`、
`expected_digest`、`task_id`、`operation_id` 和 `confirmation_token`，不能自行
缩写、猜测或从历史日志重建这些标识。

MCPX 只提供 Streamable HTTP 的 `/mcp` 端点，不提供旧版 HTTP+SSE 的 `/sse`
或 `/message` 兼容端点。

## 快速开始

### 环境要求

- Go 1.26.1 或更高版本，具体版本以 `go.mod` 为准。
- 一个需要被 MCPX 管理的本地项目目录。
- 远程访问时需要 HTTPS 反向代理或其他受信任的网络入口。

### 从源码构建

```bash
git clone https://github.com/opentokenz/mcpx.git
cd mcpx
go build -o bin/mcpx-server ./cmd/mcpx-server
```

发布构建可以关闭 CGO：

```bash
CGO_ENABLED=0 go build -o bin/mcpx-server ./cmd/mcpx-server
```

### 启动服务

```bash
./bin/mcpx-server
```

启动时注册并使用一个 Workspace：

```bash
./bin/mcpx-server --workspace /path/to/your/project
```

默认监听地址和 MCP 端点：

```text
http://127.0.0.1:9090/mcp
```

首次启动会在 `~/.mcpx/`（可用 `MCPX_HOME` 覆盖）初始化运行时目录：

| 路径 | 用途 |
| --- | --- |
| `config.yaml` | 全局监听、鉴权、安全策略和 Workspace 配置 |
| `.mcp.json` | 全局上游 MCP Server 配置 |
| `logs/` | JSONL 审计和运行日志 |
| `skills/` | 可选的本地 Skill 根目录 |
| `workspaces.example.yaml` | Workspace 配置示例 |
| `state/mcpx.db` | Remote Session、Changeset、Task、操作、快照和产物索引 |
| `tasks/` | 持久终端 Task 的日志文件 |

查看版本和命令帮助：

```bash
./bin/mcpx-server -version
./bin/mcpx-server -h
```

服务端命令包括：

```text
mcpx-server [flags]                   启动 Streamable HTTP 服务
mcpx-server workspace [flags] <name>  只读观测 Workspace 事件
mcpx-server oauth-register [url]      动态注册 OAuth 客户端
```

## 配置

### 全局配置

全局配置路径为 `~/.mcpx/config.yaml`，可用 `MCPX_HOME` 改变根目录。
下面是常用配置的最小示例：

```yaml
server:
  host: 127.0.0.1
  port: 9090

auth:
  # open | bearer | oauth | dual
  # 留空时：配置 token 则等同 bearer，否则等同 open。
  mode: open
  token: ""
  oauth:
    password: ""
    server_url: ""
    token_ttl: 86400

workspaces:
  - name: my-app
    path: /Users/you/code/my-app
    description: "业务项目"

security:
  commands:
    # allow | confirm | deny
    default: confirm
    allow:
      - ^pwd$
      - ^ls\b
      - ^git status
      - ^git diff
    confirm:
      - ^git push
      - ^docker
      - ^npm install
    deny:
      - ^rm -rf /
  files:
    max_read_bytes: 1048576
    max_patch_files: 20
    max_patch_lines: 2000
    deny:
      - ^\.git/

limits:
  max_result_bytes: 262144
```

默认值包括：监听 `127.0.0.1:9090`、文件读取上限 1 MiB、单次 Changeset
最多 20 个文件和 2000 行真实差异、工具内联结果上限 256 KiB、终端、Skill
和上游 MCP 发现默认启用。

命令策略按 `deny`、`confirm`、`allow` 和默认决策执行。显式 `deny` 或
`confirm` 规则优先于默认值。公网部署不要使用 `open`，应使用 `oauth`、
`bearer` 或 `dual`，并配置最小权限规则。

### 项目配置

项目根目录可以放置 `.mcpx.yaml`，用于覆盖项目描述、项目级安全规则、能力
开关和结果限制。进程级身份凭证不应写在项目配置中。

```yaml
description: "项目说明"

security:
  commands:
    default: confirm
```

### 上游 MCP

全局配置使用 `~/.mcpx/.mcp.json`，项目级配置使用
`{workspace}/.mcpx/.mcp.json`。项目级同名 Server 覆盖全局配置。

```json
{
  "mcpServers": {
    "github": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_TOKEN": "${GITHUB_TOKEN}"
      }
    }
  }
}
```

### Skill 发现

默认扫描 `~/.mcpx/skills`、`~/.agents/skills`、`~/.codex/skills`、
`~/.grok/skills` 和项目 `.skills`。可以在全局配置中使用
`discovery.skills.dirs` 和 `extra_dirs` 增加或替换目录。

### 状态保留

`state.retention` 负责定期回收过期的观测、Task 日志、快照和临时记录。
活跃会话、未完成 Changeset/Plan、未过期确认、有效幂等记录和仍被引用的快照
会受到保护。保留策略只在全局 `config.yaml` 中生效。

## 接入 MCP 客户端

### 本地 Bearer 客户端

```json
{
  "mcpServers": {
    "mcpx": {
      "url": "http://127.0.0.1:9090/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_TOKEN"
      }
    }
  }
}
```

将 `auth.mode` 设置为 `bearer`，并在 `auth.token` 中配置 Token。仅本机临时
调试可以使用 `open`。

### 网页端 Remote MCP

网页端通常使用 OAuth。先通过反向代理暴露 HTTPS，再配置：

```yaml
server:
  disable_localhost_protection: true
  trust_proxy_headers: true

auth:
  mode: oauth
  oauth:
    password: "换成强口令"
    server_url: "https://mcp.example.com"
```

然后将 `https://mcp.example.com/mcp` 添加到客户端。需要 ChatGPT Connector
回调注册时执行：

```bash
./bin/mcpx-server oauth-register 'https://chatgpt.com/connector/oauth/...'
# 或交互输入回调地址：
./bin/mcpx-server oauth-register
```

OAuth 发现与授权端点包括：

```text
GET  /.well-known/oauth-protected-resource
GET  /.well-known/oauth-authorization-server
POST /mcp/oauth/register
GET|POST /mcp/oauth/authorize
POST /mcp/oauth/token
```

产品不内置公网隧道服务。反向代理负责 HTTPS、域名和网络暴露，MCPX 负责
MCP 协议、鉴权和 Workspace 访问控制。

### 握手探测

不要用裸 `GET /mcp` 判断服务是否可用，使用 MCP `initialize` 请求：

```bash
curl -sS -m 5 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"0.1"}}}' \
  http://127.0.0.1:9090/mcp
```

响应中出现 `mcpx` Server 信息表示协议握手成功。

## 公开工具

`tools/list` 是工具名称、描述、参数 Schema 和 Annotation 的唯一权威来源。
当前公开工具共 31 个：

| 领域 | 工具 | 主要用途 |
| --- | --- | --- |
| Workspace | `workspace_list` | 列出已注册项目 |
| Workspace | `workspace_observe` | 读取 `changes`、`snapshot`、`diff`、`watch`、`memory` |
| Workspace | `workspace_history_read` | 按事件、请求、操作、Task、Changeset、时间和关键词查询历史 |
| Operation | `operation_batch` | 并发或按依赖 DAG 执行多个公开工具 |
| Operation | `operation_manage` | 查询、等待、读取结果、取消和恢复异步操作 |
| Session | `session_open` | 创建或恢复 Remote Session |
| Session | `session_read` | 读取会话列表、摘要和事件 |
| Session | `session_transition` | 更新、接力、接入或关闭会话 |
| Source | `source_read` | 文件、搜索、列表和上下文读取；`view` 为 `file/search/list/context` |
| Change | `change_prepare` | 校验文件操作并生成 Changeset 草稿，可按请求直接应用 |
| Change | `change_read` | 读取 Changeset diff 或历史 |
| Change | `change_discard` | 丢弃未应用草稿 |
| Change | `change_apply` | 应用已准备的 Changeset |
| Change | `change_revert` | 回滚已应用的 Changeset |
| Command | `command_run` | 执行命令或项目 Task |
| Task | `task_read` | 读取 Task 列表、状态、日志、端口和诊断 |
| Task | `task_control` | attach、stop 或向 Task 写入 stdin |
| Progress | `progress_report` | 记录公开的阶段、结果、证据和下一步 |
| Plan | `plan_create` | 创建持久化开发计划 |
| Plan | `plan_read` | 读取计划和任务状态 |
| Plan | `plan_transition` | 开始、完成、阻塞、重新规划或交付计划 |
| Runtime | `runtime_read` | 读取能力、项目摘要和适用指令 |
| Environment | `environment_read` | 读取当前环境或比较环境快照 |
| Environment | `environment_snapshot_create` | 保存环境快照 |
| Extension | `extension_discover` | 发现或描述 Skill 与上游 MCP |
| Extension | `skill_call` | 调用已发现的 Skill |
| Extension | `mcp_call` | 调用已发现的上游 MCP 工具 |
| Artifact | `artifact_read` | 列出或读取 Remote Session 产物 |
| Artifact | `artifact_register` | 注册 Workspace 文件为产物 |
| Screen | `screenshot_capture` | 截取显示器或区域 |
| Secret | `secret_provide` | 提供仅驻留内存的 Secret |

旧的 `file_read`、`context_query`、`change_execute`、`command_execute` 和
`approval_manage` 不属于当前公开工具面。文件读取统一使用 `source_read`，
文件修改使用 Changeset 工具，命令执行使用 `command_run`。

## 推荐交互流程

### 1. 建立会话和能力缓存

1. Workspace 未知时调用 `workspace_list`。
2. 调用 `session_open`，保存返回的完整 `remote_session_id`。
3. 调用 `runtime_read(view="capabilities")` 获取当前能力和工具 Schema。
4. 缓存 `tool_schema_revision`、`capability_manifest_revision`、
   `guidance_revision`、`instruction_revision`、`skill_revision` 和 `mcp_revision`。
5. 后续 `session_open` 或 `runtime_read` 传 `known_revisions`，已知版本未变化时
   直接复用本地缓存。

每次重要调用都应提供 `purpose`；`intent` 仍作为兼容别名接受。
下一次工具调用可以提供 `progress_summary`，写入上一调用已验证的结果和下一步，
不要写入隐藏思维链。没有下一次工具调用、需要等待用户或发生阻塞时，使用
`progress_report`。

### 2. 读取源码

```json
{
  "remote_session_id": "rs_...",
  "view": "file",
  "path": "src/main.ts",
  "mode": "window",
  "purpose": "读取入口文件并确认当前版本"
}
```

`source_read(view="file")` 返回：

- `sha256`：后续 Changeset 的 `base_sha256`。
- `format.charset`：字符集，例如 `utf-8`。
- `format.bom`：BOM 状态。
- `format.line_ending` 和 `line_ending_counts`：`LF`、`CRLF`、`CR` 或 `mixed`。
- `format.final_newline`：文件是否以换行符结尾。
- `truncated`、`offset`、`limit` 和 `next_action`：窗口读取状态。

生成变更时必须保留这些格式元数据。完整预览使用单个 `path` 和
`mode="full"`；完整模式返回 `mime_type`、完整 SHA-256 和原始格式。源码扩展名
优先按文本处理，例如 TypeScript 返回 `text/typescript`，不会因系统 MIME 表
把 `.ts` 误判为 `video/mp2t`。图片使用 MCP `ImageContent`，二进制文件使用
Base64 数据。

已知多个文件时，使用同一次 `source_read(view="file", items=[...])` 批量读取，
并通过 `max_total_bytes` 控制总预算。搜索、列表和上下文读取分别使用
`view="search"`、`view="list"` 和 `view="context"`，不要为了确认一个已知路径
先重复列目录。

### 3. 修改文件

默认修改路径如下：

```text
source_read → change_prepare → change_read(diff) → change_apply
```

其中：

- `change_prepare` 默认只生成 draft，不直接改 Workspace。
- 用户已经明确授权且希望减少往返时，可以传 `apply=true`；策略或语义确认
  仍要求后续确认。
- `change_read(view="diff")` 用于审阅文件变更，完整 Diff 也可通过 Resource 读取。
- `change_apply` 必须原样携带 `changeset_id` 和 `expected_digest`。
- `change_discard` 用于放弃旧草稿，避免反复读取历史清理状态。
- `change_revert` 用于回滚已应用的 Changeset。

示例：

```json
{
  "remote_session_id": "rs_...",
  "summary": "更新首页标题",
  "purpose": "根据用户要求修改首页标题",
  "idempotency_key": "req-update-home-title-1",
  "operations": [
    {
      "operation": "update",
      "path": "src/App.vue",
      "base_sha256": "sha256:...",
      "patch": "@@ -8,1 +8,1 @@\n-  <h1>旧标题</h1>\n+  <h1>新标题</h1>"
    }
  ]
}
```

`update.patch` 必须是标准 Unified Diff，不是 `apply_patch` 的
`*** Begin Patch` 格式。也可以使用 `replace_exact`、`insert_before`、
`insert_after`、`delete_exact` 和 `replace_range` 做局部精确修改。

带相同 `idempotency_key` 的重试会复用原 Changeset。即使客户端没有传幂等键，
同一 Remote Session 中相同摘要和内容摘要的活动 draft 也会复用，避免一次变更
准备出多个重复草稿。

普通变更会保持原文件字符集、BOM、换行和末尾换行状态。若变更导致文件格式
改变，服务端返回 `FORMAT_CHANGED`；只有明确要求格式化时才使用 `format=true`。
版本冲突、Patch 上下文不匹配和策略错误都会返回结构化 `error.code`、
`retryable`、`recovery` 或 `next_action`，客户端应按结构化字段恢复，不要盲目
重复提交同一请求。

### 4. 执行命令和 Task

```json
{
  "remote_session_id": "rs_...",
  "command": "go test ./internal/server -count=1",
  "purpose": "验证本次服务端变更"
}
```

`command_run` 默认等待短命令完成；超过等待窗口时返回 `task_id`。后续应直接
使用已知 ID：

```text
task_read(view="status", task_id="task_...")
task_read(view="logs", task_id="task_...", stdout_offset=0, stderr_offset=0)
task_control(operation="attach", task_id="task_...", yield_time_ms=30000)
```

不要为了确认已知 Task 反复调用 `task_read(view="list")`。只有 Task ID 丢失或
服务端明确返回 `not_found` 时才重新列出 Task。首次 list 返回
`task_list_digest`，后续带 `known_task_digest`；摘要未变化时服务端返回
`not_modified=true` 和空任务数组。

### 5. 批量和异步操作

需要并行读取或执行多个相互独立的工具时使用 `operation_batch`；有依赖时在
`depends_on` 中声明步骤 ID。使用 `operation_manage`：

```text
operation_manage(action="wait", operation_id="op_...")
operation_manage(action="result", operation_id="op_...")
```

不要通过重复 `status` 轮询等待同一个操作；运行中的操作会返回一次性的
`next_action`，通常应直接执行一次 `wait`。`operation_manage` 的批量模式只接受
`operation_ids`，且仅支持 `status` 和 `result`，不能把它再次嵌套进
`operation_batch`。

当异步操作内部执行 `command_run` 时，MCPX 会等待其终端 Task 进入最终状态后
再记录 `operation.completed`。`wait` 和 `result` 返回的顶层结果及步骤结果
会展开 MCP 包装，客户端不需要再解析嵌套的 `content[].text`。

### 6. 统一响应

工具默认文本适合模型和宿主直接展示；机器结果保存在 ARC 元数据
`_meta["mcpx.result"]`。响应状态包括：

| 状态 | 含义 |
| --- | --- |
| `succeeded` | 操作已成功完成 |
| `accepted` | 已交给持久化 Operation 或 Task，需按 ID 继续 |
| `waiting_confirmation` | 需要先向用户展示摘要并获得明确确认 |
| `interrupted` | 执行被中断，但可根据返回 ID 查询状态 |
| `failed` | 业务、策略、版本或运行时失败，应按结构化错误恢复 |

大结果不会在多个字段中重复镜像。Changeset Diff、Task 日志和 Artifact 可通过
以下 Resource URI 读取：

```text
mcpx://remote-sessions/{remote_session_id}/changesets/{changeset_id}/diff
mcpx://remote-sessions/{remote_session_id}/tasks/{task_id}/logs
mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}
```

## 本机终端观测

服务运行期间，可以在另一个终端只读观察指定 Workspace：

```bash
./bin/mcpx-server workspace my-app
```

命令使用本机 Socket 订阅服务端事件，不启动第二个 HTTP 服务，不执行工具、命令
或文件修改。启动时回放历史事件，随后实时接收事件；断线后按 sequence 补偿。

可用参数：

```text
-history int       回放最近事件数量，范围 1-200，默认 100
-format text|json  文本或一行一个 JSON 事件，默认 text
-detail            显示语义用途、操作 ID 和执行事实
-diff summary|preview|full
-tool string       按工具过滤
-status string     按事件状态过滤
-operation string  按 operation_id 过滤
-path string       按文件路径过滤
```

示例：

```bash
./bin/mcpx-server workspace -format json -history 200 my-app
./bin/mcpx-server workspace -detail -diff full -tool change_apply my-app
```

文本模式按一次工具调用聚合为一个交互块，使用 `Read`、`Edited`、`Ran`、
`Searched` 等语义动作，并压缩重复的内部操作步骤。Diff 展示会区分新增、删除
和上下文；支持真彩色终端时使用更明确的前景色和低饱和背景色，普通终端降级为
ANSI 16 色。设置 `NO_COLOR=1` 关闭颜色，`COLORTERM=truecolor` 或 `24bit`
启用真彩色，`COLUMNS` 可显式指定终端宽度。

机器处理日志时使用 `--format json`，不要解析文本中的颜色、缩进或装饰边框。
事件中保留 `event_id`、`sequence`、`request_id`、`operation_id`、`task_id`、
`changeset_id`、状态、耗时、路径、命令和截断标志等字段。

## 安全与数据边界

- `open` 只适合本机临时调试；公网必须使用 `bearer`、`oauth` 或 `dual`。
- 反向代理场景按需启用 `disable_localhost_protection` 和
  `trust_proxy_headers`，并限制可信 `allowed_origins`。
- Remote Session 使用 `viewer`、`editor`、`approver` 和 `owner` 角色。
- 命令和文件都经过策略匹配；命令可允许、要求确认或拒绝，文件变更还会检查
  SHA-256、路径和差异预算。
- `confirmation_token` 只表达用户对同一业务参数的语义确认，不是认证凭据。
- `secret_provide` 的明文值只在进程内短期使用，不写入 SQLite、Workspace 或日志。
- SQLite、Task 日志、OAuth 客户端注册和 Token 密钥位于 `~/.mcpx/`，运行时使用
  受限文件权限；不要把真实 Token、密码或 Secret 写入仓库和命令字符串。
- 截图默认通过 MCP 返回，不写入 Workspace 或 SQLite。
- `state.retention` 会清理过期过程事件、Task、快照和临时记录，但保护活跃会话
  和未完成交付状态。

## 项目开发

仓库结构：

```text
cmd/mcpx-server       服务入口、workspace 观测和 oauth-register
internal/server       HTTP Gateway、公开工具和 Resource 注册
internal/changeset    Changeset 准备、应用、回滚和历史
internal/operation    异步 Operation、依赖调度和结果分页
internal/terminal     Task 生命周期、日志、端口和诊断
internal/source       搜索、列表、上下文和批量读取
internal/observation  事件存储、本机观测和终端渲染
internal/auth         Bearer、OAuth、Principal 和 ACL
internal/config       全局/项目配置及 MCP/Skill 发现
docs/plans             实现计划
docs/specs             设计规格
```

提交前运行：

```bash
gofmt -w ./cmd ./internal
test -z "$(gofmt -l ./cmd ./internal)"
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
CGO_ENABLED=0 go build -o bin/mcpx-server ./cmd/mcpx-server
git diff --check
```

更多分支、Pull Request 和发布要求见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可与使用边界

本项目使用 [Apache License 2.0](LICENSE)。MCPX 面向学习、研究和获得授权的
开发环境自动化。使用者需要自行确认 Workspace、命令、凭证、网络入口和数据的
授权范围；在生产环境使用前应完成安全评估、备份、最小权限配置和人工确认流程
验证。本文档不构成安全、法律、医疗、财务或其他专业建议。
