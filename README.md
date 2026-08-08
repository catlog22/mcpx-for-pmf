# MCPX

MCPX 是运行在本地开发环境中的 MCP Runtime（网关）。它通过
Streamable HTTP 把本地 Workspace、源码、变更、命令、任务、环境和扩展能力
安全地提供给 ChatGPT、Claude、Cursor、Grok 等 MCP 客户端。

MCPX 的重点不是增加一个聊天界面，而是提供一套可审计、可恢复、对模型友好
的开发工具协议：客户端可以跨连接恢复同一个 Remote Session，模型可以用文件 SHA、
Edit ID、Task ID、discovery revision 和能力版本避免重复读取和无效重试。

## 能力概览

| 能力 | 说明 |
| --- | --- |
| Remote Session | 持久化 Workspace 会话、角色权限、事件、接力和跨客户端恢复 |
| Workspace | 注册多个项目，并在创建会话时显式绑定项目根目录 |
| Source | 文件窗口、批量读取、搜索、文件列表和有界上下文；返回 SHA-256 与编码/换行元数据 |
| Edit | 精确 replacement、批量变更、原子写、SHA 校验和格式保留 |
| Terminal | 执行命令或项目 Task；短命令内联返回，长命令持久化为 Task |
| Operation | 并行或有依赖地执行多个公开工具，并统一等待、分页、取消和恢复 |
| Project Task | 从项目配置中发现测试、构建和检查任务，并解析诊断信息 |
| Workspace State | 读取 Git 状态、快照、差异、监听结果和项目记忆 |
| Environment | 查看操作系统、架构、Shell、容器、资源、文件系统和工具链 |
| Extension | 发现并调用配置的 Skill 与上游 MCP Server |
| Artifact | 注册、列出和分页读取测试报告、构建产物、覆盖率和日志 |
| Screenshot | 截取显示器或屏幕区域，并通过 MCP ImageContent 返回 |
| Security | OAuth、Bearer、Remote Session ACL、命令/文件策略和语义确认 |
| Observation | 通过本机 Socket 观察工具调用、Task、Edit 和操作事件 |

## 设计边界

MCPX 同时处理两类 Session：

| 标识 | 生命周期 | 用途 |
| --- | --- | --- |
| `Mcp-Session-Id` | Streamable HTTP 传输层临时标识 | 连接和协议状态，重连或换客户端后可能变化 |
| `remote_session_id` | SQLite 持久化业务标识 | Workspace、角色、Edit、Task、Plan、操作、快照和产物的主键 |

客户端应始终原样保存并复用服务端返回的 `remote_session_id`、`edit_id`、`plan_id`、
`plan_task_id`、`execution_task_id`、`operation_id`、`artifact_id`、`discovery_id` 和
`discovery_revision`，不能自行缩写、猜测或从历史日志重建这些标识。Plan Task 与
执行 Task 是不同命名空间，不存在兼容的通用 `task_id` 字段。

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

注册或更新一个 Workspace：

```bash
./bin/mcpx-server workspace register /path/to/your/project
```

然后启动服务：

```bash
./bin/mcpx-server
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
| `state/mcpx.db` | Remote Session、Edit、Task、Plan、操作、快照和产物索引 |
| `tasks/` | 持久终端 Task 的日志文件 |

查看版本和命令帮助：

```bash
./bin/mcpx-server -version
./bin/mcpx-server -h
```

服务端命令包括：

```text
mcpx-server [flags]                   启动 Streamable HTTP 服务
mcpx-server observe [flags] <name>    终端只读观测 Workspace 事件
mcpx-server workspace register <path> 注册或更新 Workspace
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

默认值包括：监听 `127.0.0.1:9090`、窗口读取返回上限 1 MiB、显式 full 源文件上限
4 MiB、单次 Edit 最多 20 个文件和 2000 行真实差异、工具内联结果上限 256 KiB、
终端、Skill 和上游 MCP 发现默认启用。

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
活跃会话、未完成 Plan、未过期确认、有效幂等记录和仍被引用的快照
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

然后将 `https://mcp.example.com/mcp` 添加到客户端（ChatGPT / Codex Remote MCP）。

OAuth 对齐 [OpenAI MCP 鉴权约定](https://developers.openai.com/plugins/build/auth) 与
MCP Authorization（OAuth 2.1 + PKCE）：

- 资源元数据：`/.well-known/oauth-protected-resource`（及 `/mcp` 路径形态）
- 授权服务器元数据：`/.well-known/oauth-authorization-server`（含
  `client_id_metadata_document_supported: true`，优先 CIMD）
- DCR：`POST /mcp/oauth/register`（CIMD 不可用时仍可用）
- 授权 / 换票：`/mcp/oauth/authorize`、`/mcp/oauth/token`（`resource` 参数 + S256）

ChatGPT 使用 **CIMD**（`client_id` 为 `https://chatgpt.com/oauth/...` 文档 URL）时，
服务端会拉取并校验元数据，并接受
`https://chatgpt.com/connector/oauth/{callback_id}` 与旧回调
`https://chatgpt.com/connector_platform_oauth_redirect`。
仍可用手动 DCR 预注册：

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
当前公开工具共 17 个，分为 10 个 core tools 和 7 个 support tools：

| 领域 | 工具 | 主要用途 |
| --- | --- | --- |
| Core | `session` | open、attach、close Remote Session |
| Core | `read` | 文件、搜索、列表、上下文和环境读取 |
| Core | `edit` | 精确 replacement、批量编辑、原子写和格式保留 |
| Core | `observe` | session、task、history、changes、logs、diff 观察 |
| Core | `execute` | 命令或项目 Task 执行，以及 attach、stop、stdin |
| Core | `plan` | create、read、advance、complete、block、replan、deliver |
| Core | `artifact` | 产物登记、列表和分片读取 |
| Core | `discover` | 显式发现 Skill 或上游 MCP，并签发 discovery lease |
| Core | `skill_call` | 调用已由 `discover` 返回的 Skill |
| Core | `mcp_call` | 调用已由 `discover` 返回的上游 MCP 工具 |
| Support | `operation_batch` | 并发或按依赖 DAG 执行多个工具 |
| Support | `operation_manage` | 查询、等待、读取结果、取消和恢复异步操作 |
| Support | `runtime_read` | 读取能力、项目摘要和适用指令 |
| Support | `environment_read` | 读取当前环境或比较环境快照 |
| Support | `environment` | 保存环境快照 |
| Support | `screenshot_capture` | 截取显示器或区域 |
| Support | `secret_provide` | 提供仅驻留内存的 Secret |

所有有状态工具统一使用完整的 `remote_session_id`；`tools/list`、session
bootstrap、capability manifest 和 recovery action 使用同一组名称与 Schema。

## 推荐交互流程

### 1. 建立会话和能力缓存

1. Workspace 已知时直接调用 `session(action="open")`。
2. 保存返回的完整 `remote_session_id`；新客户端用 `session(action="attach")` 接力。
3. 调用 `runtime_read(view="capabilities")` 获取当前能力和工具 Schema。
4. 缓存 `tool_schema_revision`、`capability_manifest_revision`、
   `guidance_revision`、`instruction_revision`、`skill_revision` 和 `mcp_revision`。
5. 后续 `session(action="open")` 或 `runtime_read` 传 `known_revisions`，已知版本未变化时
   直接复用本地缓存。

每次重要调用都可以提供统一的语义上下文：`goal` 表示总体目标，`purpose` 表示本次
操作作用，`reasoning_summary` 表示可公开的简短判断依据，`progress_summary` 表示
已验证进展，`next_step` 表示下一项具体计划；`plan_id`、`plan_task_id`、`execution_task_id`、`operation_id` 用于绑定执行上下文。
`reasoning_summary` 不是隐藏思维链，不得写入私有推理过程。上述字段会原样进入 ARC
`structuredContent.context`、`_meta["mcpx.result"].mcpx.result.context` 和持久化观测事件。
没有下一次工具调用、需要等待用户或发生阻塞时，直接在响应中说明状态和下一步。

终端观测使用普通 stdout/stderr pipe，不依赖 PTY、tmux 或 ConPTY。观测事件先写入
durable Store，再通过本地 JSONL 帧推送；`observe --format=json`、终端 text 渲染和
其他 JSONL 客户端消费同一事件。事件的 `phase` 表示 `action_started`、`output`、
`result` 或 `error`，语义上下文字段会在 JSONL、历史和 text observer 中保留。`call_id` 缺省回退为
`request_id` 用于内部请求关联；不同客户端接力时使用同一个 `remote_session_id`，并可结合
`workspace` 调用 `observe(view="history")` 查询历史操作。`plan_task_id`、`execution_task_id`、`operation_id` 负责各自的计划、执行和操作归属。

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

`read(view="file")` 返回：

- `sha256`：后续 edit 的 `base_sha256`，始终针对原始文件字节。
- `format.charset`：字符集，例如 `utf-8`。
- `format.bom`：BOM 状态。
- `format.line_ending` 和 `line_ending_counts`：`LF`、`CRLF`、`CR` 或 `mixed`。
- `format.final_newline`：文件是否以换行符结尾。
- `truncated`、`offset`、`limit` 和 `next_action`：窗口读取状态。

生成变更时必须保留这些格式元数据。带 UTF-16 BOM 的文件会先以 Unicode 文本呈现，
写回时恢复原字符集和 BOM。完整预览使用单个 `path` 和
`mode="full"`；完整模式返回 `mime_type`、完整 SHA-256 和原始格式。源码扩展名
优先按文本处理，例如 TypeScript 返回 `text/typescript`，不会因系统 MIME 表
把 `.ts` 误判为 `video/mp2t`。图片使用 MCP `ImageContent`，二进制文件使用
Base64 数据。

已知多个文件时，使用同一次 `read(view="file", items=[...])` 批量读取，
并通过 `max_total_bytes` 控制总预算。搜索、列表和上下文读取分别使用
`view="search"`、`view="list"` 和 `view="context"`，不要为了确认一个已知路径
先重复列目录。

`read(view="list")` 同时返回两类结果：`files` 是递归普通文件分页；`entries` 是当前
scope 的第一层条目，并以 `kind=file|directory|symlink|other` 标记类型，不会展开目录内容。
清理“只保留某文件”的 Workspace 前，先读取根 scope 的 `entries`：只有
`entries_complete=true` 且 `entries_policy_filtered=false` 时才能把它当作完整可见根清单。
若存在 `entries_next_cursor`，按 `entries_next_action` 读取其余第一层条目；不要从
`files` 的首个分页推断顶层目录。

### 3. 修改文件

默认修改路径如下：

```text
read(view="file") → edit → observe(view="changes")
```

`edit` 接收 `edits[]`，支持 create、update、rename；用户提出删除、移除或清理时必须使用专用的
`move_out_prepare → submit_move_out` 流程，目标会安全移至隔离区而非永久删除，update 优先使用
精确唯一 `replacements`。同一请求带 `idempotency_key` 时，重试返回原终态，
参数变化返回 `IDEMPOTENCY_CONFLICT`。默认只返回有界 diff 预览；需要完整内容时
使用 `observe(view="diff", edit_id, offset, limit)` 分片读取。

生产限制和破坏性操作契约：完整 `read` 的源文件上限为 4 MiB，超大文件使用
`mode="window"` 流式读取；单次 `read.items` 最多 20 项；`operation_batch` 最多
32 步；`edit` 最多 1000 条真实变更行。`read(view="list", path=...)` 的 `path`
是硬作用域。

`edit` 只支持 create、update、rename；删除、移除或清理必须使用两阶段的
`move_out_prepare → submit_move_out`，禁止通过 `execute`、shell、glob 或 symlink 绕过：

1. `move_out_prepare` 接收 Workspace 内明确的 `file`、`directory` 或 `symlink` 目标。它只校验
   目录根路径，不扫描、哈希或返回目录子项；file 冻结 SHA，symlink 冻结链接文本摘要。响应只返回
   最多 20 个显式目标预览、总目标数和 manifest SHA，因此数万文件目录不会撑大模型上下文。
   `purpose` 必须先表达最终语义：仅检查就写“仅 prepare/仅预览”并停止；用户请求删除、移除或清理时
   则从 prepare 开始明确写“安全移至隔离区”，不能在 submit 时临时改写语义。
2. 服务端返回 `confirmation_uuid`、`move_request_id`、`manifest_sha256` 和原始
   `idempotency_key` 供展示和审计；`submit_move_out_arguments` 只包含
   `remote_session_id` 与 `confirmation_uuid` 两个提交参数。
3. 网页端模型向用户展示冻结清单并询问；用户确认后，模型将
   `submit_move_out_arguments` 原样提交给 `submit_move_out`。
4. `submit_move_out` 按 UUID 从服务端取回并重新校验 Workspace 范围、manifest、文件 SHA、过期时间和
   purpose；客户端无法覆盖这些冻结值。明确的 `directory` 是移出根，服务端以受 Workspace 同级 Root 约束的原子 rename 移至
   Workspace 父目录下的 `.mcpx-quarantine/<move_request_id>/`；返回的 `quarantine_path` 相对此父目录。
   `symlink`（包括目录确认后被替换的 symlink）只移动链接入口，
   绝不跟随目标。提交写入持久化审计，并返回可恢复的隔离路径。

安全移出能力支持明确指定的非空目录树和 symlink；目录内的 symlink、特殊文件不会阻断移动，也不会在
prepare 时被逐项核验或回传，但
absolute path、`..` 越界和中间 symlink 仍拒绝。`submit_move_out`
在 MCP `tools/list` 中声明 `readOnlyHint=false`、`destructiveHint=false`、
`idempotentHint=true` 和 `openWorldHint=false`，并发布仅限注册 Workspace、仅文件系统、
无 shell、revision guarded、同级隔离区原子移动、可恢复和持久化审计等机器可读约束。

示例：

```json
{
  "remote_session_id": "rs_...",
  "purpose": "根据用户要求修改首页标题",
  "idempotency_key": "req-update-home-title-1",
  "edits": [
    {
      "operation": "update",
      "path": "src/App.vue",
      "base_sha256": "sha256:...",
      "replacements": [
        {"match": "<h1>旧标题</h1>", "replacement": "<h1>新标题</h1>"}
      ]
    }
  ]
}
```

普通变更保持原文件字符集、BOM、换行和末尾换行状态。版本冲突、匹配失败和策略
错误都会返回结构化 `error.code`、`retryable`、`recovery` 或 `next_action`。

### 4. 执行命令和 Task

```json
{
  "remote_session_id": "rs_...",
  "action": "run",
  "command": "go test ./internal/server -count=1",
  "purpose": "验证本次服务端变更",
  "scope": "workspace",
  "yield_time_ms": 10000
}
```

`execute(action="run")` 默认等待短命令完成；超过等待窗口时返回 `execution_task_id`。后续
使用已知 ID：

```text
observe(view="status", execution_task_id="task_...")
observe(view="logs", execution_task_id="task_...", stdout_offset=0, stderr_offset=0)
execute(action="attach", execution_task_id="task_...", yield_time_ms=30000)
```

长命令的状态和日志使用 observe 的 offset/next offset 续读；输出被截断时响应会
直接给出下一次调用模板。`stop` 和 `stdin` 通过 `execute(action="stop|stdin")`
完成，并重新执行权限与 Workspace 校验。

`execution_mode="async"` 只表示把本次工具调用提交为异步 Operation，不保证立即返回
`execution_task_id`。命令是否脱离为持久化执行 Task 由 `yield_time_ms` 决定：命令在等待窗口内结束时，
Operation 结果直接包含 `completed_in_call=true`；需要 Task 生命周期时，应设置小于预期
运行时长的 `yield_time_ms`，再使用返回的 `execution_task_id` 调用 `observe` 或 `execute(action="attach")`。

### 5. Plan、Artifact 与扩展

`plan(action="create|read|advance|complete|block|replan|deliver")` 只引用服务端
返回的 `plan_id`、`plan_task_id` 和结构化 evidence；`create.tasks[].local_id` 仅在创建请求内解析依赖。典型路径是：

```text
plan(create) → plan(advance) → edit/execute → artifact(register) → plan(complete) → plan(deliver)
```

产物使用 `artifact(action="register|list|read")`；注册时持久化 `source_encoding`/`source_bom`，读取时
`source_offset`/`next_source_offset` 始终使用源文件 byte 坐标。UTF-8/UTF-16 文本通过
`delivery_encoding=utf-8` 返回 `text`，二进制通过 `delivery_encoding=base64` 返回 `base64`，不会把任意字节伪装成文本。

Skill/MCP 必须先显式发现：

```text
discover(kind="skill", view="describe", name="...") → skill_call(... discovery_id, discovery_revision)
discover(kind="mcp", view="describe", server="...", include_tools=true) → mcp_call(... discovery_id, discovery_revision)
```

跳过 discover 会得到 `DISCOVERY_REQUIRED`，其中明确包含
`required_call_count=1`、`discovery_required=true` 和下一次 discover 参数；这次
额外调用是公开的交互成本，不由服务端隐式完成。revision 失效时重新 discover。

### 6. 批量和异步操作

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

当异步操作内部执行 `execute` 时，MCPX 会等待其终端 Task 进入最终状态后
再记录 `operation.completed`。`wait` 和 `result` 返回的顶层结果及步骤结果
会展开 MCP 包装，客户端不需要再解析嵌套的 `content[].text`。

### 6. 统一响应

工具默认文本适合模型和宿主直接展示；机器结果同时保存在
`structuredContent` 和 ARC 元数据 `_meta["mcpx.result"]`。响应状态包括：

`tools/list` 同时为 MCPX 工具公布 `outputSchema`，其描述的是实际返回的
`structuredContent` 公共结构（`status`、`type`、`context`、`data`、`error`、`hints`、`actions`），
并对有硬上限的工具通过 `x-mcpx-limits` 发布与 `runtime_read(view="capabilities").limits` 同源的限制。
`runtime_read(view="capabilities")` 的 `runtime` 同时给出 `version`、`build_commit`、`build_time`、
`tool_schema_revision` 和 capability 版本信息；旧的顶层 revision alias 不再返回。

| 状态 | 含义 |
| --- | --- |
| `succeeded` | 操作已成功完成 |
| `accepted` | 已交给持久化 Operation 或 Task，需按 ID 继续 |
| `waiting_confirmation` | 需要先向用户展示摘要并获得明确确认 |
| `interrupted` | 执行被中断，但可根据返回 ID 查询状态 |
| `failed` | 业务、策略、版本或运行时失败，应按结构化错误恢复 |

大结果不会在多个字段中重复镜像。Task 日志和 Artifact 可通过以下 Resource URI
读取；Edit Diff 使用 `observe(view="diff")` 分页：

```text
mcpx://remote-sessions/{remote_session_id}/tasks/{execution_task_id}/logs
mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}
```

ARC 的机器结果固定包含 `context`：

```json
{
  "context": {
    "goal": "修复终端观测体验",
    "purpose": "验证命令执行结果",
    "reasoning_summary": "先确认最小执行链路",
    "progress_summary": "命令已完成并返回 exit_code=0",
    "next_step": "检查异常任务恢复",
    "plan_id": "pl_...",
    "plan_task_id": "pt_...",
    "execution_task_id": "task_...",
    "operation_id": "op_..."
  }
}
```

终端文本会将这些字段压缩为三组：目标与作用、判断/进展/下一步、计划/任务/操作
标识；普通结果不会因为只有内部 `operation_id` 而额外生成 Context 区块。

## 本机终端观测

服务运行期间，可以在另一个终端只读观察指定 Workspace：

```bash
./bin/mcpx-server observe my-app
```

命令使用本机 Socket 订阅服务端事件，不启动第二个 HTTP 服务，不执行工具、命令
或文件修改。启动时回放历史事件，随后实时接收事件；断线后按 sequence 补偿。

可用参数：

```text
-history int       回放最近事件数量，范围 1-100，默认 100
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
./bin/mcpx-server observe -format json -history 100 my-app
./bin/mcpx-server observe -detail -diff full -tool edit my-app
```

文本模式使用紧凑的行式 CLI 输出：先显示 `Read`、`Edited`、`Ran`、`Searched`
等语义动作，再缩进显示 Context（目标、作用、判断依据、进展、下一步）、执行事实和结果；命令 stdout/stderr 按流合并
并带行号。内部 `operation.*` 调度事件、重复的远端 `*.started`/`*.completed`
notice 默认静默，只保留失败、取消等对人有用的最终结果，不再绘制大块边框。
ARC 人类展示层会按工具使用稳定的动作色（读取/发现、编辑、执行、计划、会话等），
再按状态覆盖为运行中、等待确认、失败或中断色；相邻操作块之间显示带工具和状态的细分隔线。
这些颜色和分隔线只属于 text observer 的人类展示；Context 字段进入 ARC JSON、structuredContent 和持久化机器事件，但颜色与分隔线不进入机器协议。
超长输出按终端宽度换行并设置正文预算，超出时提示改用 JSON 查看完整事件。Diff
展示会区分新增、删除和上下文；支持真彩色终端时使用更明确的前景色和低饱和背景色，
普通终端降级为 ANSI 16 色。文本模式会清理命令输出中的 ANSI/C0 控制字符；设置
`NO_COLOR=1` 关闭颜色，`COLORTERM=truecolor` 或 `24bit` 启用真彩色，`COLUMNS`
可显式指定终端宽度。

机器处理日志时使用 `--format json`，不要解析文本中的颜色、缩进或装饰边框。
事件中保留 `event_id`、`sequence`、`request_id`、`operation_id`、`plan_task_id`、
`execution_task_id`、`edit_id`、状态、耗时、路径、命令和截断标志等字段。

## 安全与数据边界

- `open` 只适合本机临时调试；公网必须使用 `bearer`、`oauth` 或 `dual`。
- 反向代理场景按需启用 `disable_localhost_protection` 和
  `trust_proxy_headers`，并限制可信 `allowed_origins`。
- Remote Session 使用 `viewer`、`editor`、`approver` 和 `owner` 角色。
- 命令和文件都经过策略匹配；命令可允许、要求确认或拒绝，文件变更还会检查
  SHA-256、路径和差异预算。
- `user_confirmed` 只表达用户对同一业务参数的语义确认；服务端保存待确认摘要，
  不要求模型复制确认 token。
- `secret_provide` 的明文值只在进程内短期使用，不写入 SQLite、Workspace 或日志。
- SQLite、Task 日志、OAuth 客户端注册和 Token 密钥位于 `~/.mcpx/`，运行时使用
  受限文件权限；不要把真实 Token、密码或 Secret 写入仓库和命令字符串。
- 截图默认通过 MCP 返回，不写入 Workspace 或 SQLite。
- `state.retention` 会清理过期过程事件、Task、快照和临时记录，但保护活跃会话
  和未完成交付状态。

## 项目开发

仓库结构：

```text
cmd/mcpx-server       服务入口、observe 终端观测、workspace 注册和 oauth-register
internal/server       HTTP Gateway、公开工具和 Resource 注册
internal/edit         Edit 解析、原子写、格式保留和变更摘要
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

## Star History

<a href="https://www.star-history.com/?repos=opentokenz%2Fmcpx&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=opentokenz/mcpx&type=date&theme=dark&legend=top-left&sealed_token=jUtxc1OYmFK08WQj99XkmFzM0HRA-hpQB7I9wHBLMBGHx-67q1wA2YAs4xsVkz5atYfU4hBNzBeZ1PgKY6SZM1t4MY6U70cFpKG49h7I-p1HEzbjWiJMh5EIJ2wl7Mc4ihBZ05TXuvpgxIR_0SppHmEn18A66kOXgnljlPGZm18kCP52p6jPzPM1hH_v" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=opentokenz/mcpx&type=date&legend=top-left&sealed_token=jUtxc1OYmFK08WQj99XkmFzM0HRA-hpQB7I9wHBLMBGHx-67q1wA2YAs4xsVkz5atYfU4hBNzBeZ1PgKY6SZM1t4MY6U70cFpKG49h7I-p1HEzbjWiJMh5EIJ2wl7Mc4ihBZ05TXuvpgxIR_0SppHmEn18A66kOXgnljlPGZm18kCP52p6jPzPM1hH_v" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=opentokenz/mcpx&type=date&legend=top-left&sealed_token=jUtxc1OYmFK08WQj99XkmFzM0HRA-hpQB7I9wHBLMBGHx-67q1wA2YAs4xsVkz5atYfU4hBNzBeZ1PgKY6SZM1t4MY6U70cFpKG49h7I-p1HEzbjWiJMh5EIJ2wl7Mc4ihBZ05TXuvpgxIR_0SppHmEn18A66kOXgnljlPGZm18kCP52p6jPzPM1hH_v" />
 </picture>
</a>

## 致谢

感谢 [LINUX DO](https://linux.do) 社区：**学 AI，上 LINUX DO。**

---

## 故障排查

- 启动后检查日志中的 `endpoint`、鉴权模式和 inventory（Workspace / Skill / MCP）。
- 客户端连不上时，先确认 URL 是 `/mcp`、客户端支持 Streamable HTTP、端口一致且 Token 有效。
- `401`：检查 `Authorization`、OAuth issuer、resource URL 和 `server_url`。
- `/sse` 或 `/mcp/sse` 返回 `404`：这是预期行为；请把客户端改为 Streamable HTTP `/mcp`。
- 客户端看不到新工具：刷新 MCP Server，必要时新建客户端会话以重新获取工具表。
- 截图失败：检查桌面会话、录屏权限和 Linux 截图后端。
