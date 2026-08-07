package server

// Domain ownership for the public MCP tool surface (file-cluster layout).
// Full subpackages under tools/<domain> are deferred: handlers remain *Runtime
// methods so they can share envelope/auth/session helpers without export churn.
//
// Domain              | Public tools                         | Primary files
// --------------------|--------------------------------------|---------------------------
// workspace           | workspace_read                       | tools_public_adapters.go, tools_workspace_*.go, runtime.go
// session             | session, session_read                | tools_public_adapters.go, tools_session_open.go, tools_remote_session.go, tools_manage.go
// source              | source_read                          | tools_public_adapters.go, tools_source*.go
// change              | change, change_read                  | tools_public_adapters.go, tools_changeset.go, tools_change_execute.go
// command / task      | command_run, task, task_read         | tools_public_adapters.go, tools_command_execute.go
// plan                | plan, plan_read                      | tools_public_adapters.go, tools_plan.go
// operation           | operation_batch, operation_manage    | tools_operation.go, operation_runtime.go
// runtime / env       | runtime_read, environment_read, environment | tools_public_adapters.go, tools_environment.go, tools_instruction.go
// extension           | extension_discover, skill_call, mcp_call | tools_public_adapters.go, tools_ext.go
// artifact            | artifact, artifact_read              | tools_public_adapters.go, tools_artifact.go (+ internal/artifact)
// screenshot / secret | screenshot_capture, secret_provide   | tools_screenshot.go, tools_manage / secrets
// catalog / prompts   | tools/list registration              | tools_catalog.go, prompts/, guidance/
//
// Dispatch entry points live in tools_public_adapters.go (toolChange, toolSession, …).
