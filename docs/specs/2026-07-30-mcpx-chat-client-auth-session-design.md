# MCPX 聊天客户端鉴权与会话隔离设计

- **日期：** 2026-07-30
- **状态：** 已批准（主目标：网页端聊天会话可链接此 MCP）
- **范围：** 鉴权（OAuth 2.1 + 静态 Bearer）+ HTTP 会话隔离与结果契约
- **非范围：** tunnel 脚本、外部 IdP、CIMD、Landlock/Docker 沙箱、工具面改成固定编程目录

## 1. 背景与目标

### 1.0 主目标（验收标准）

**在网页端聊天产品（如 Claude.ai 自定义 MCP / 同类 Web Connector）的会话里，填入 MCPX 的公网 HTTPS 地址，即可完成 OAuth 授权并调用本机/服务器上的 MCPX 工具。**

不依赖桌面客户端手写 Header，不依赖产品内置 `tunnel.sh`（公网暴露由用户现有 frp/反代/云主机完成）。

### 1.1 问题

MCP 规范（2025-11-25）HTTP 鉴权基于 OAuth 2.1：Protected Resource Metadata、Authorization Server 发现、客户端注册（含 Dynamic Client Registration）、授权码 + PKCE、协议层 HTTP 401。

**网页端聊天**通常：

- 只让用户填 **Remote MCP URL**，由客户端自动走 OAuth，**不能**像 Grok/Cursor 桌面配置那样稳定依赖静态 `headers.Authorization`；
- 回调 `redirect_uri` 为 **厂商 HTTPS 域名**（非仅 localhost）；
- 授权页在用户浏览器打开，需可公网访问的 MCPX origin；
- 可能对 CORS / 预检、正确的 `resource` / `issuer` URL 更敏感。

MCPX 当前仅支持**应用层静态 Bearer**（工具内 `requireAuth`），缺少：

- `/.well-known/oauth-protected-resource` 与 AS metadata
- 协议层 401 + `WWW-Authenticate`
- OAuth 授权码流与 DCR
- 面向 Web 聊天的「只配 URL → 浏览器口令授权 → 会话内用工具」路径

参考实现：[coding-tools-mcp](https://github.com/xyTom/coding-tools-mcp) 的进程内 OAuth + Bearer 双模式（**不**引入其 tunnel.sh）。

### 1.2 目标

1. **主路径：** 网页端聊天会话通过标准 Remote MCP + OAuth 链接 MCPX（发现 → DCR → 口令授权 → JWT → 工具调用）。
2. **辅路径：** 桌面/IDE 客户端仍可用静态 `Authorization: Bearer <token>`（`bearer` / `dual`）。
3. 多网页会话 / 多客户端状态隔离；工具大输出有界、可预期。
4. 保持 MCPX 差异化：多 workspace 网关、Terminal First、上游 MCP/Skill 代理。

### 1.3 非目标

- 不做 `tunnel.sh` / 内置 cloudflared / ngrok（用户自备 HTTPS 入口即可）。
- 不做 Client ID Metadata Documents（CIMD）——以 DCR 服务 Web 客户端为主。
- 不做外部 Authorization Server / 用户账号体系（授权确认 = 运维口令页）。
- 不把工具表改成 coding-tools 的 20 工具固定目录。
- 不引入 OS 级沙箱（Landlock 等）——可后续单独立项。
- 不保证某一家网页产品私有扩展字段；以 MCP 2025-11-25 Authorization 最小互通集为准，并在 M2/M4 用至少一种真实 Web 客户端 dogfood。

### 1.4 成功标准

| # | 标准 |
|---|------|
| **S0（主）** | 在**网页端聊天**中添加 Remote MCP = `https://<公网>/mcp`，完成浏览器口令授权后，该会话可列出并调用 MCPX 工具 |
| S1 | 任意符合规范的 OAuth MCP 客户端：仅配 base/MCP URL，完成 DCR+口令授权后可 `tools/list` / 调工具 |
| S2 | 仅配置静态 Bearer 的桌面客户端在 `bearer` / `dual` 下行为与现网兼容 |
| S3 | 两路客户端（或两个 Web 会话）并行时，workspace 选择与审批互不串扰 |
| S4 | 超大终端输出被截断并带 `truncated`（或等价）标志，不无限膨胀 |
| S5 | 未授权访问 `/mcp` 返回 **HTTP 401**（非仅 envelope 内 unauthorized） |
| S6 | 公网部署下 `auth.oauth.server_url` 固定后，metadata 中的 issuer/resource 与 JWT `iss`/`aud` 一致，Web 客户端不因错误 origin 失败 |

## 2. 方案选择

采用 **方案 1：进程内迷你 AS + 强化会话**。

- 同一 MCPX 进程兼任 Resource Server 与 Authorization Server（个人 Runtime 场景）。
- 授权确认方式：**运维口令**（启动配置或首次生成打印），无浏览器用户账号——适合「我家/我的 VPS 上的 Runtime，我在网页聊天里授权一次」。
- 公网可达性依赖用户现有反代 / frp / 云主机 HTTPS，产品不捆绑隧道。
- **Web 主路径默认推荐 `auth.mode: dual` 或 `oauth`**，并**必须**配置 `auth.oauth.server_url` 为浏览器可打开的公网 origin。

备选（已否决）：仅 401 协议壳、外置 IdP。

## 3. 鉴权设计

### 3.1 模式

配置字段 `auth.mode`：

| 值 | 行为 |
|----|------|
| `open` | 不要求凭证。非 loopback 绑定启动时 **警告**；推荐仅本机 |
| `bearer` | 仅接受静态 `auth.token`（及项目级覆盖，见 3.5） |
| `oauth` | 仅接受本 AS 签发的 access token（JWT） |
| `dual` | 静态 Bearer **或** 合法 OAuth JWT 均可（**推荐远程默认**） |

环境变量可覆盖（实现时与现有 `MCPX_*` 风格对齐），例如 `MCPX_AUTH_MODE`。

### 3.2 协议层门禁

- 所有对 MCP 端点（默认 `/mcp`）的受保护方法（POST 等）在进入 JSON-RPC 前校验凭证。
- 失败：**HTTP 401**，响应头包含：

  ```http
  WWW-Authenticate: Bearer realm="mcpx",
    resource_metadata="<absolute-url>/.well-known/oauth-protected-resource"
  ```

  可选带 `scope="mcp"`（或实现选定的默认 scope）。
- well-known 与 `/mcp/oauth/*` 端点：**不**要求已登录（authorize 靠口令页；register/token 按 OAuth 规则）。
- 工具层保留防御性 `requireAuth`：与 HTTP 层策略一致；避免「仅工具失败、握手却成功」的半开状态。HTTP 已拒绝则工具不应在无凭证下执行。

### 3.2.1 网页客户端配套（CORS 与安全头）

Web 聊天厂商多数由**其后端**调 MCP（浏览器只参与 OAuth 跳转）；仍需覆盖：

| 项 | 要求 |
|----|------|
| CORS | 对 `/mcp`、`/.well-known/*`、`/mcp/oauth/register`、`/mcp/oauth/token` 等 API：可配 `server.allowed_origins`（逗号列表或 `*` 仅建议调试）；默认对常见预检 `OPTIONS` 返回 204 + 允许方法/头（含 `Authorization`、`Content-Type`、`Mcp-Session-Id`、`MCP-Protocol-Version`） |
| Authorize 页 | `GET/POST /mcp/oauth/authorize` 为浏览器文档导航，不依赖 CORS；页面简洁、移动端可输入口令 |
| Cookie | 授权流**不**依赖 MCPX 侧登录 Cookie；口令仅 POST 一次换 code |
| HTTPS | 文档要求公网必须 HTTPS（反代终结 TLS）；MCPX 可仍听 HTTP loopback/内网，由反代转发 |
| Host 防护 | 公网反代场景继续支持 `disable_localhost_protection` |

### 3.3 发现与 OAuth 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/.well-known/oauth-protected-resource` | RFC 9728；含 `authorization_servers`、`resource`、`scopes_supported` |
| GET | `/.well-known/oauth-authorization-server` | RFC 8414；issuer、authorize/token/registration 端点、PKCE S256、`code_challenge_methods_supported` |
| POST | `/mcp/oauth/register` | RFC 7591 Dynamic Client Registration |
| GET | `/mcp/oauth/authorize` | 授权页（HTML 表单） |
| POST | `/mcp/oauth/authorize` | 提交口令 + 授权参数，成功则 redirect 带 `code` |
| POST | `/mcp/oauth/token` | `grant_type=authorization_code` + PKCE verifier → access token |
| GET（可选） | `/.well-known/mcp.json` | server card：名称、版本、auth 类型提示 |

**Canonical resource URL**

- 优先配置 `auth.oauth.server_url`（或 `MCPX_SERVER_URL`）：公网稳定 origin，**不含**路径歧义时在规格中固定一种：
  - **约定：** `server_url` 为 origin（如 `https://mcp.example.com:29090`），resource 标识为 `{server_url}/mcp`。
- 未配置时：从当前请求推导 origin（注意：默认 **不信任** `X-Forwarded-*`，除非显式 `server.trust_proxy_headers: true`）。
- JWT 的 `iss` = origin（或 AS issuer URL，与 AS metadata 的 `issuer` 一致）；`aud` = resource URL（`{origin}/mcp`）。实现须在 metadata 与 token 校验中保持一致并写单测固定。

### 3.4 动态注册（DCR）

- `redirect_uris` 必填、去重、数量上限（如 ≤ 10）。
- 允许：`https:`（**网页聊天厂商回调为主路径**）；`http:` 仅 `localhost` / `127.0.0.1` / `::1`（桌面/本地调试）。
- DCR 为 Web 客户端默认注册方式：运营者**不必**事先知道 Claude/厂商的 client_id。
- `grant_types` 仅支持 `authorization_code`；`response_types` 仅 `code`。
- `token_endpoint_auth_method`：`none` | `client_secret_post` | `client_secret_basic`。
- 服务端生成 `client_id`；confidential 时生成 `client_secret`（仅返回一次，存储 digest）。
- 注册表 **进程内存**；上限（如 1024）；重启后动态客户端需重新注册。
- 可选预注册：配置 `auth.oauth.client_id` + `redirect_uris` + 可选 secret（运维已知客户端）。

**不实现** CIMD（Client ID Metadata Documents）。

### 3.5 口令授权流

1. 客户端打开 `/mcp/oauth/authorize?...`（含 `client_id`、`redirect_uri`、`code_challenge`、`code_challenge_method=S256`、`state`、`resource` 等）。
2. 服务端校验 client、redirect **精确匹配**、PKCE challenge 形态。
3. 展示 HTML：client 名、redirect、请求的 resource/scope；口令输入框。
4. 用户输入 `auth.oauth.password`（配置为空则启动时生成 `token_urlsafe` 并 **打印到日志**，只一次可见性依赖运维）。
5. 口令正确：签发一次性 authorization code（TTL 如 5 分钟），redirect 回客户端。
6. 客户端 `POST /mcp/oauth/token`：code + `code_verifier` + client 认证方式 → JWT access token。
7. 之后 MCP 请求：`Authorization: Bearer <access_token>`。

**Access token**

- 算法：HS256；密钥 `auth.oauth.token_secret`（hex，≥32 字节）；空则进程内随机（重启后旧 token 失效）。
- Claims 至少：`iss`、`aud`、`exp`、`iat`、`client_id`（或 `sub`=client_id）、`scope`（如 `mcp`）。
- 校验：签名、exp、iss、aud、client 仍在 registry。
- TTL：可配，默认 86400 秒；合理上下限（如 60–604800）。

**静态 Bearer**

- `auth.token` 与 OAuth 均为进程级身份认证，只从全局配置读取；项目配置不覆盖身份凭证。
- 在 `bearer` / `dual` 下：header 与 required 常量时间比较（`subtle.ConstantTimeCompare` 或等价）。
- 在 `oauth` 模式下忽略静态 token（即使配置了也不接受），避免误开双通道。
- **OAuth 口令与 JWT 为服务级**，不随 workspace 切换；workspace 级 static token 仅影响 static bearer 路径。

### 3.6 与 Host 防护

- 保留 `server.disable_localhost_protection`。
- 公网部署文档：强 token/口令 + 建议 `server_url` + dual/oauth/bearer，禁止默认 `open`。

## 4. 会话与结果契约

### 4.1 会话模型

- `Mcp-Session-Id` 只由 Streamable HTTP 协议层使用，不保存 Workspace、审批、任务或默认 cwd。
- 业务状态主键为 SQLite 持久化的 `remote_session_id`。
- `workspace_select`、`file_read` 和 `file_patch` 不再暴露；源码读取使用 `code_read`，源码修改使用 Changeset。
- 查询型 Workspace / MCP / Skill 能力要求显式 `workspace` 或 `remote_session_id`；执行型能力要求 `remote_session_id`。

### 4.2 生命周期

| 项 | 建议默认 |
|----|----------|
| `transport.session_idle_ttl` | 1h |
| 空闲回收 | 由 mcp-go 回收 transport session，不影响 Remote Session |
| 显式结束 | `DELETE /mcp` 只结束 transport session；`remote_session_close` 关闭业务会话 |

### 4.3 跨 session 安全

- `approval_*`、`terminal_logs` / `terminal_kill` 等操作必须显式携带 `remote_session_id`。
- Principal 必须是 Remote Session 成员，角色还要满足读、编辑或审批权限，否则返回 `forbidden` / `not_found`。
- 单测覆盖：更换 transport session 后状态不丢失；非成员不能读取或操作会话资源。

### 4.4 结果有界

- 配置 `limits.max_result_bytes`（默认如 200_000）。
- 对终端 stdout/stderr、源码窗口等文本字段：超出则截断，envelope / `structured` 中设 `truncated: true`，并提供分页读取或 Resource Link。
- Workspace 不写入 transport session。推荐调用序为：`workspace_list` → `remote_session_create` / `remote_session_list` → 显式携带 `remote_session_id` 的开发工具。

## 5. 配置草图

```yaml
server:
  host: 127.0.0.1
  port: 9090
  disable_localhost_protection: false
  trust_proxy_headers: false   # 仅在受信反代后开启
  allowed_origins: []          # CORS；空 = 反射/宽松策略在实现中二选一并写死，推荐默认 echo 请求 Origin 仅当在列表，Web dogfood 可临时加厂商源

auth:
  mode: dual                 # 网页远程推荐 dual 或 oauth
  token: ""                  # 桌面静态 bearer；网页主路径可不配
  oauth:
    password: ""             # empty => generate on start, log once（授权页输入）
    server_url: ""           # 【网页必配】公网 origin，e.g. https://mcp.example.com
    token_ttl: 86400
    token_secret: ""         # hex; empty => ephemeral process key
    # optional preregistered client:
    # client_id: ""
    # client_secret: ""
    # redirect_uris: []

transport:
  session_idle_ttl: 1h

limits:
  max_result_bytes: 200000
```

**网页端推荐最小配置：**

```yaml
server:
  disable_localhost_protection: true   # 若反代 Host 非 loopback
auth:
  mode: oauth                          # 或 dual
  oauth:
    password: "<强口令>"
    server_url: "https://<你的公网域名或IP:端口>"
```

首次启动若启用 oauth/dual 且 password 为空：日志打印 `oauth authorize password: ...`（用户在网页跳转的授权页输入该口令）。

## 6. 模块边界

| 包 | 职责 |
|----|------|
| `internal/auth` | 静态 Bearer；JWT 校验入口；mode 解析；与 context 头注入 |
| `internal/oauth`（新） | Client registry、PKCE、code 存储、token 签发/校验、metadata DTO、口令页 HTML |
| `internal/server` | 挂载 well-known/oauth 路由；HTTP 401 中间件；与 Streamable HTTP 组合；启动日志 |
| mcp-go transport session | max/TTL/prune；仅负责 Streamable HTTP 协议状态，不参与资源归属 |
| `internal/envelope` 或 server 辅助 | 统一截断 |

依赖：JWT 库（如 `github.com/golang-jwt/jwt/v5`）；现有 `mark3labs/mcp-go`。

## 7. 分期交付

| 里程碑 | 内容 | 验收 |
|--------|------|------|
| **M1** | 协议层 401 + PRM/AS metadata 骨架 + 静态 Bearer 在 HTTP 层生效 | curl 无 token → 401；有 static token → initialize 成功 |
| **M2** | DCR + 口令 authorize + token + dual/oauth | 手工或集成测完整 OAuth 一轮后调 tool |
| **M3** | Session max/TTL/跨 session 拒绝 + 结果封顶 | 单测 + 大输出截断 |
| **M4** | README：**网页端聊天链接 Remote MCP** 步骤 + 桌面 Bearer；公网 `server_url` / 反代注意 | 按文档可在一种 Web 聊天里完成 S0 |

建议实现顺序严格 M1→M2→M3→M4；**M1/M2 优先打通网页 OAuth**（S0）。

## 8. 测试计划

- 单元：`CheckBearer`、JWT 校验（错 aud/过期/错密钥）、PKCE、redirect 校验、DCR 边界。
- 单元：跨 session 资源拒绝；截断辅助。
- 集成（可用 `httptest`）：unauth 401 → metadata → register → authorize(password) → token → MCP initialize + 一工具。
- 回归：`auth.mode=open` loopback；`bearer` only；项目配置不能覆盖全局 static token。
- **验收 dogfood（M4）：** 至少一种网页聊天（如 Claude.ai 自定义 MCP / 当时可用的 Web Connector）添加 `https://…/mcp`，口令授权后工具可用（S0）。若某厂商暂时不开放自定义 MCP，用规范 OAuth 客户端或 MCP Inspector 等价走通，并在 README 标注已验证客户端列表。

## 9. 文档与运维

- README 增补：**网页端链接步骤**（公网 HTTPS → 配 `server_url` + oauth/dual → 聊天里填 URL → 浏览器输口令 → 回会话用工具）、鉴权模式表、桌面 Bearer、禁止公网 `open`。
- 明确：**不做 tunnel**；远程用现有反代/frp/云主机即可。
- 安全提示：口令与 token 勿提交仓库；OAuth 注册表内存语义；token_secret 轮换导致全员重登；网页授权等于把 Runtime 能力交给持 token 的聊天会话，口令需够强。

## 10. 风险与缓解

| 风险 | 缓解 |
|------|------|
| 聊天客户端 OAuth 实现差异 | 对齐 2025-11-25 最小集；M2 用 1–2 个真实客户端 dogfood |
| 反向代理 origin 错误 | 强制可配 `server_url`；默认不信任转发头 |
| 口令泄露即等同根权限 | 文档强调；后续可加失败限速（非本规格必做） |
| mcp-go 自定义路由能力 | M1  spike：若中间件难挂，用前置 `http.ServeMux` 包装 Streamable server |

## 11. 决议记录

| 项 | 决议 |
|----|------|
| **主目标** | **网页端聊天会话可链接此 MCP（Remote URL + OAuth）** |
| 优先级 | 鉴权（A）+ 会话/结果（B）；A/M2 服务主目标 S0 |
| 授权 UX | 运维口令页（非无口令、非外置 IdP） |
| 隧道 | 不做（用户自备 HTTPS 入口） |
| 架构 | 进程内 AS + dual/oauth；DCR 服务 Web 厂商回调 |
| 规格路径 | `docs/specs/`（`docs/superpowers/` 已 gitignore） |
