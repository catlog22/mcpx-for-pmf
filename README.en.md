# MCPX

**An MCP Runtime connecting AI to local development environments.**

MCPX is an **MCP Runtime (gateway)** for development environments. ChatGPT, Claude, Cursor, Grok, and other MCP clients that support Streamable HTTP can use one consistent tool surface to inspect projects, review Unified Diffs, modify source code, run tasks, collect environment information, and call local MCP servers and Skills.

Development state is stored in SQLite-backed Remote Sessions. It is independent of any AI vendor or a single `Mcp-Session-Id`, so different clients can query, authorize, hand off, and continue the same development session.

**Documentation:** [中文（默认）](README.md) · English

## Features

| Area | Description |
| --- | --- |
| **Remote Session** | Persistent SQLite sessions, ACLs, and one-time handoff tokens across clients and transports. |
| **Workspace** | Register multiple projects and bind each Remote Session to an explicit project. |
| **Terminal** | Run short commands or persistent tasks, inspect logs and ports, attach, and stop tasks. |
| **Source and Changeset** | Read source with SHA-256 revisions, review Unified Diffs, detect conflicts, apply atomically, and roll back. |
| **Project Task** | Discover project-defined test, build, and check tasks and parse structured diagnostics. |
| **Environment** | Inspect OS, architecture, kernel, display, container, shell, resources, filesystem, and toolchain. |
| **Extensions** | Proxy upstream MCP servers and discover or execute local Skills. |
| **Security and Audit** | OAuth / Bearer authentication, principals, session ACLs, command and file policies, approvals, and JSONL audit logs. |

## Quick start

### 1. Install

Download the archive for your platform from [Releases](https://github.com/opentokenz/mcpx/releases), or build from source:

```bash
git clone https://github.com/opentokenz/mcpx.git
cd mcpx
go build -o bin/mcpx ./cmd/mcpx-server
```

MCPX requires **Go 1.26.1 or later**; the exact development version is defined by `go.mod`.

### 2. Start

```bash
./bin/mcpx
# Or register a project while starting:
./bin/mcpx --workspace /path/to/your/project
```

The first start creates runtime data under **`~/.mcpx/`**. Set `MCPX_HOME` to use another location. The default endpoint is:

```text
http://127.0.0.1:9090/mcp
```

MCPX provides Streamable HTTP only; legacy HTTP+SSE endpoints are not supported.

Check the version with:

```bash
./bin/mcpx -version
```

## Configuration overview

The global configuration is stored at `~/.mcpx/config.yaml`:

```yaml
server:
  host: 127.0.0.1
  port: 9090

auth:
  # mode: open | bearer | oauth | dual
  mode: ""
  token: "" # Static Bearer token
  oauth:
    password: "" # If empty, generated and printed at startup
    server_url: "" # Public origin, required for web OAuth

security:
  commands:
    # Fallback for commands that match no rule: allow | confirm | deny
    default: allow
    allow:
      - ^ls\b
      - ^git status
    confirm:
      - ^git push
      - ^docker
    deny:
      - ^rm -rf /
  files:
    max_read_bytes: 1048576
    max_patch_files: 20
    max_patch_lines: 2000
    deny:
      - ^\.git/
```

The default command policy is `allow`. A command matched by a `confirm` rule still requires explicit approval through `approval_manage` before execution. Do not expose `open` mode to the public internet; use HTTPS, a strong OAuth password, and least-privilege policies.

Projects can also be registered with:

```bash
./bin/mcpx --workspace /path/to/your/project
```

## Client integration

For web clients that support Remote MCP and OAuth, expose MCPX through an HTTPS reverse proxy and configure `auth.mode: oauth` or `dual`, `oauth.password`, and `oauth.server_url`. Add the remote URL ending in `/mcp`; the client can complete dynamic client registration and authorization.

For a local client using a static Bearer token:

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

To verify the endpoint, send an MCP `initialize` request rather than relying on a bare `GET`:

```bash
curl -sS -m 5 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"0.1"}}}' \
  http://127.0.0.1:9090/mcp
```

## Tool surface

The main tools include `workspace_list`, `session_open`, `file_read`, `context_query`, `change_execute`, `command_execute`, `session_manage`, `change_manage`, `plan_manage`, `task_manage`, `runtime_inspect`, `environment_inspect`, `workspace_state`, `extension_manage`, `artifact_manage`, `approval_manage`, `screenshot_capture`, and `secrets_provide`.

All direct file modifications go through `change_execute`. The recommended workflow is:

1. Open or resume a Remote Session.
2. Query or read the relevant source and its revision.
3. Create and review a Changeset and Unified Diff.
4. Apply the change through `change_execute`.
5. Run tests or project tasks with `command_execute` and inspect the resulting state.

## Security boundaries

- `open` is intended for local use only; use `bearer`, `oauth`, or `dual` for authenticated access.
- Remote Session roles include `viewer`, `editor`, `approver`, and `owner`.
- Secret values are kept in process memory and are not written to SQLite, logs, or the workspace.
- Runtime state, credentials, task logs, and audit logs are stored under `~/.mcpx/` with restricted permissions.
- Never place real tokens, passwords, or secrets in this repository or in command strings.
- Review commands and diffs before approving changes, especially when exposing MCPX beyond localhost.

## Future

- **Presentation**: Improve host capability negotiation so clients can select `diff`, `table`, `tree`, or `diagram` views while retaining a safe text fallback.
- **ARC**: Evolve result types and JSON Schemas compatibly, with version negotiation, error recovery, and consistent action descriptions across clients.
- **Large-result delivery**: Unify paginated and streamed Resource Link delivery for diffs, logs, search results, and artifacts to reduce inline response size.
- **Observability**: Extend trace, latency, and result-classification metrics to diagnose client rendering, approval flows, and task execution.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for the branch, pull request, protected `main`, validation, and release conventions. `main` is the protected branch; changes enter it through a pull request and are not pushed directly.

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
test -z "$(gofmt -l ./cmd ./internal)"
go build -o bin/mcpx ./cmd/mcpx-server
```

The `v0.1.0` release is built from the verified `main` commit. Future releases are created from `main` after the pull request and CI checks have passed.

## Learning and research disclaimer

MCPX is provided for learning, research, and authorized development-environment automation only. Users are responsible for deployment, configuration, command execution, file changes, credential handling, and any direct or indirect consequences. Do not use MCPX against systems, data, or networks without authorization. Before production use, perform a security review, back up relevant data, apply least-privilege policies, and verify human approval flows.

This project and its documentation are not security, legal, medical, financial, or other professional advice, and they are not guaranteed to fit any particular use case. Confirm the authorization scope and review commands and changes before operating on a real environment.

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=opentokenz/mcpx&type=Date)](https://www.star-history.com/#opentokenz/mcpx&Date)

## Acknowledgements

Thanks to the [LINUX DO](https://linux.do) community: **Learn AI, join LINUX DO.**

## License

This project is licensed under the [Apache License 2.0](LICENSE).
