package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/approval"
	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/projecttask"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
	"mcpx/internal/terminal"
)

const defaultCommandYield = 10 * time.Second

// toolCommandExecute uses one Task implementation for both ordinary commands
// and discovered project tasks. It waits for short commands, but only exposes
// a task_id to clients when the process still runs after the yield window.
func (r *Runtime) toolCommandExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	purpose, scope, intentErr := commandIntent(envReq.Payload)
	if intentErr != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", intentErr.Error())
	}
	command, _ := envReq.Payload["command"].(string)
	command = strings.TrimSpace(command)
	if taskName, _ := envReq.Payload["task"].(string); taskName != "" {
		if command != "" {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "command and task are mutually exclusive")
		}
		discovered, ok := projecttask.Find(remote.WorkspacePath, taskName)
		if !ok {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "task_not_found", fmt.Sprintf("project task %q not found", taskName))
		}
		command = discovered.Command
	}
	if command == "" {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "command or task is required")
	}
	commandDigest := commandRequestDigest(envReq.RequestID, remote.ID, remote.WorkspaceName, command, purpose, scope)
	effective := r.effectiveConfig(remote.WorkspacePath)
	if !effective.Terminal.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "terminal tools are disabled")
	}
	decision := security.MatchCommand(effective.Security.Commands, command)
	switch decision {
	case security.Deny:
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "command_execute", Command: command, Status: "denied", Detail: commandExecutionDetail(purpose, scope, commandDigest)})
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "denied", "command denied by policy")
	case security.Confirm:
		yield := commandYield(envReq.Payload)
		approvalID, approvalErr := r.approvals.Put(approval.Pending{
			Tool: "command_execute", Summary: command, Command: command,
			CommandYieldMs: int(yield / time.Millisecond), Purpose: purpose, Scope: scope,
			CommandDigest: commandDigest, WorkDir: remote.WorkspacePath,
			RequestID: envReq.RequestID, Workspace: remote.WorkspaceName,
			RemoteSessionID: remote.ID, PrincipalID: principal.ID,
		})
		if approvalErr != nil {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "approval_store_error", approvalErr.Error())
		}
		response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, remote.WorkspaceName,
			map[string]any{
				"approval_id": approvalID, "command": command, "purpose": purpose,
				"scope": scope, "command_digest": commandDigest,
				"next_action": nextAction("approval_manage", map[string]any{
					"remote_session_id": remote.ID, "action": "approve", "approval_id": approvalID,
				}),
			}, "APPROVAL_REQUIRED", "command execution requires approval")
		response.RemoteSessionID = remote.ID
		return r.resultJSON(response)
	}

	return r.executeCommandTask(ctx, envReq, principal, remote, command, commandYield(envReq.Payload), purpose, scope, commandDigest)
}

func (r *Runtime) executeCommandTask(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session, command string, yield time.Duration, purpose, scope, commandDigest string) (*mcp.CallToolResult, error) {
	task, err := r.tasks.StartRemote(ctx, remote.ID, remote.WorkspaceName, remote.WorkspacePath, command)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "start_error", err.Error())
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: remote.ID, Type: "command.started", OperationID: task.ID, Summary: command, Metadata: commandExecutionDetail(purpose, scope, commandDigest)})
	waitCtx, cancel := context.WithTimeout(ctx, yield)
	completed := task.Wait(waitCtx)
	cancel()
	data := r.taskResultData(task, 0, 0)
	data["purpose"] = purpose
	data["scope"] = scope
	data["command_digest"] = commandDigest
	data["workspace_scoped"] = scope == "workspace"
	capTaskExecutionOutput(data, config.MaxResultBytes(r.cfg.Limits))
	if completed {
		data["completed_in_call"] = true
		data["task_id"] = ""
		detail := commandExecutionDetail(purpose, scope, commandDigest)
		detail["exit_code"] = data["exit_code"]
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "command_execute", Command: command, Status: "ok", Detail: detail})
		result := compactToolResult(data, commandOutputText(data, fmt.Sprintf("Command completed with exit code %v.", data["exit_code"])))
		return result, nil
	}
	data["completed_in_call"] = false
	data["next_action"] = nextAction("task_manage", map[string]any{
		"remote_session_id": remote.ID, "action": "attach", "task_id": task.ID,
		"stdout_offset": data["stdout_next_offset"], "stderr_offset": data["stderr_next_offset"],
		"yield_time_ms": int(yield / time.Millisecond),
	})
	detail := commandExecutionDetail(purpose, scope, commandDigest)
	detail["task_id"] = task.ID
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "command_execute", Command: command, Status: "running", Detail: detail})
	result := compactToolResult(data, commandOutputText(data, fmt.Sprintf("Command is running as Task %s.", task.ID)))
	return result, nil
}

// commandOutputText renders the model-facing summary with any stdout/stderr
// inline, so completed command output is readable directly in the conversation.
// Truncated streams state the truncation and point to task_manage for more.
func commandOutputText(data map[string]any, summary string) string {
	var builder strings.Builder
	builder.WriteString(summary)
	for _, stream := range []string{"stdout", "stderr"} {
		text, _ := data[stream].(string)
		if text == "" {
			continue
		}
		builder.WriteString("\n\n")
		builder.WriteString(stream)
		builder.WriteString(":\n")
		builder.WriteString(text)
	}
	if truncated, _ := data["output_truncated"].(bool); truncated {
		builder.WriteString("\n\nOutput truncated; call task_manage with action=attach or logs to read more.")
	}
	return builder.String()
}

func capTaskExecutionOutput(data map[string]any, maxBytes int) {
	const hardCap = 256 << 10 // 256 KiB inline budget
	if maxBytes <= 0 || maxBytes > hardCap {
		maxBytes = hardCap
	}
	for _, stream := range []string{"stdout", "stderr"} {
		value, _ := data[stream].(string)
		trimmed, truncated := TruncateUTF8(value, maxBytes)
		if !truncated {
			continue
		}
		data[stream] = trimmed
		data[stream+"_truncated"] = true
		offset, _ := data[stream+"_offset"].(int)
		data[stream+"_next_offset"] = offset + len(trimmed)
		data["output_truncated"] = true
	}
}

func commandIntent(payload map[string]any) (purpose, scope string, err error) {
	purpose, _ = payload["purpose"].(string)
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		return "", "", fmt.Errorf("purpose is required and must describe the user's requested development action")
	}
	if len(purpose) > 512 {
		return "", "", fmt.Errorf("purpose exceeds 512 bytes")
	}
	scope, _ = payload["scope"].(string)
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "workspace"
	}
	if scope != "workspace" {
		return "", "", fmt.Errorf("unsupported execution scope %q; only workspace is allowed", scope)
	}
	return purpose, scope, nil
}

func commandRequestDigest(requestID, remoteSessionID, workspace, command, purpose, scope string) string {
	value := strings.Join([]string{requestID, remoteSessionID, workspace, command, purpose, scope}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func commandExecutionDetail(purpose, scope, commandDigest string) map[string]any {
	return map[string]any{"purpose": purpose, "scope": scope, "command_digest": commandDigest, "workspace_scoped": scope == "workspace"}
}

func commandYield(payload map[string]any) time.Duration {
	yield := intPayload(payload, "yield_time_ms")
	if yield <= 0 {
		return defaultCommandYield
	}
	if yield > 60_000 {
		yield = 60_000
	}
	return time.Duration(yield) * time.Millisecond
}

func (r *Runtime) toolTaskManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	edit := action == "stop" || action == "stdin"
	envReq, principal, remote, fail := r.changeRequest(ctx, req, edit)
	if fail != nil {
		return fail, nil
	}
	if action == "list" {
		items, err := r.tasks.List(remote.ID, intPayload(envReq.Payload, "limit"))
		if err != nil {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "task_list_error", err.Error())
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"tasks": items})
	}
	taskID, _ := envReq.Payload["task_id"].(string)
	task, err := r.tasks.Get(remote.ID, taskID)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "not_found", err.Error())
	}
	switch action {
	case "status":
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, task.StatusView())
	case "logs":
		data := r.taskResultData(task, intPayload(envReq.Payload, "stdout_offset"), intPayload(envReq.Payload, "stderr_offset"))
		if int64(data["stdout_next_offset"].(int)) < task.LogStreamSize("stdout") || int64(data["stderr_next_offset"].(int)) < task.LogStreamSize("stderr") {
			data["next_action"] = nextAction("task_manage", map[string]any{"remote_session_id": remote.ID, "action": "logs", "task_id": task.ID, "stdout_offset": data["stdout_next_offset"], "stderr_offset": data["stderr_next_offset"]})
		}
		result := compactToolResult(data, commandOutputText(data, fmt.Sprintf("Task %s log chunk returned.", task.ID)))
		return result, nil
	case "attach":
		waitCtx, cancel := context.WithTimeout(ctx, commandYield(envReq.Payload))
		task.Wait(waitCtx)
		cancel()
		data := r.taskResultData(task, intPayload(envReq.Payload, "stdout_offset"), intPayload(envReq.Payload, "stderr_offset"))
		stdoutNext := data["stdout_next_offset"].(int)
		stderrNext := data["stderr_next_offset"].(int)
		if fmt.Sprint(task.StatusView()["status"]) == string(terminal.TaskRunning) || int64(stdoutNext) < task.LogStreamSize("stdout") || int64(stderrNext) < task.LogStreamSize("stderr") {
			data["next_action"] = nextAction("task_manage", map[string]any{"remote_session_id": remote.ID, "action": "attach", "task_id": task.ID, "stdout_offset": stdoutNext, "stderr_offset": stderrNext, "yield_time_ms": int(commandYield(envReq.Payload) / time.Millisecond)})
		}
		result := compactToolResult(data, commandOutputText(data, fmt.Sprintf("Task %s attached.", task.ID)))
		return result, nil
	case "stop":
		if err := task.Kill(); err != nil {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "stop_error", err.Error())
		}
		_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: remote.ID, Type: "task.stopped", OperationID: task.ID, Summary: task.Command})
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, task.StatusView())
	case "ports":
		ports, err := terminal.ListeningPorts(ctx, task.PID)
		if err != nil {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "port_inspection_unavailable", err.Error())
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"task_id": task.ID, "pid": task.PID, "ports": ports})
	case "diagnostics":
		log, next := task.Logs(0)
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"task_id": task.ID, "diagnostics": projecttask.ParseDiagnostics(log, intPayload(envReq.Payload, "limit")), "parsed_log_bytes": next})
	case "stdin":
		input, _ := envReq.Payload["input"].(string)
		if err := task.WriteStdin(input); err != nil {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "stdin_unavailable", err.Error())
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"task_id": task.ID, "accepted_bytes": len(input)})
	default:
		return r.invalidAction(ctx, req, "task_manage", action)
	}
}

func (r *Runtime) taskResultData(task *terminal.Task, stdoutOffset, stderrOffset int) map[string]any {
	stdout, stdoutNext := task.LogsFor("stdout", stdoutOffset)
	stderr, stderrNext := task.LogsFor("stderr", stderrOffset)
	data := task.StatusView()
	data["task_id"] = task.ID
	data["stdout"] = stdout
	data["stderr"] = stderr
	data["stdout_offset"] = stdoutOffset
	data["stderr_offset"] = stderrOffset
	data["stdout_next_offset"] = stdoutNext
	data["stderr_next_offset"] = stderrNext
	return data
}
