package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
// an execution_task_id to clients when the process still runs after the yield window.
func (r *Runtime) toolCommandExecute(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	purpose, scope, intentErr := commandIntent(envReq)
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
			return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "task_not_found", fmt.Sprintf("project task %q not found", taskName))
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
	analysis := security.AnalyzeCommand(effective.Security.Commands, command)
	decision := analysis.Decision
	switch decision {
	case security.Deny:
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "command_execute", Command: command, Status: "denied", Detail: commandExecutionDetail(purpose, scope, commandDigest, analysis)})
		message := "command denied by policy after auditing all command segments"
		if containsUnsafeShellFeature(command) {
			message += "；命令包含不支持的 shell 特性。&&、|| 和 ; 可以组合并会先分段审计；管道、重定向、单个 &、换行、$() 和反引号命令替换仍会被拒绝。对于这些不支持的特性，请改用可独立审计的简单命令，例如 git fetch && git rev-parse HEAD && git status。"
		}
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "denied", message)
	case security.Confirm:
		yield := commandYield(envReq.Payload)
		confirmationToken := stringPayload(envReq.Payload, "confirmation_token")
		if isCleanCoreRequest(ctx) {
			userConfirmed := boolPayload(envReq.Payload, "user_confirmed")
			pending, pendingOK := r.pendingCommandConfirmation(remote.ID, principal.ID, command, scope, commandDigest)
			if !userConfirmed || !pendingOK {
				if !pendingOK {
					var confirmationErr error
					pending, confirmationErr = r.approvals.PutPending(approval.Pending{
						Tool: "command_execute", Summary: command, Command: command,
						CommandYieldMs: int(yield / time.Millisecond), Purpose: purpose, Scope: scope,
						CommandDigest: commandDigest, WorkDir: remote.WorkspacePath,
						RequestID: envReq.RequestID, Workspace: remote.WorkspaceName,
						RemoteSessionID: remote.ID, PrincipalID: principal.ID,
						ContentKey: cleanCommandConfirmationContentKey(principal.ID, commandDigest),
					})
					if confirmationErr != nil {
						return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "confirmation_store_error", confirmationErr.Error())
					}
				}
				confirmationData := map[string]any{
					"command": command, "purpose": purpose, "scope": scope,
					"command_digest": commandDigest, "pending_digest": commandDigest,
					"command_policy":        commandPolicyData(analysis),
					"confirmation_required": true, "user_confirmed_required": true,
					"summary": "组合命令的全部 segment 已完成策略预检；请向用户展示整条命令及用途，确认后将 user_confirmed=true 原样重试。",
				}
				response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, remote.WorkspaceName,
					confirmationData, "USER_CONFIRMATION_REQUIRED", "命令执行等待用户语义确认")
				response.RemoteSessionID = remote.ID
				addRecoveryAction(&response, "execute", "用户确认后使用相同 command/task、purpose 和 remote_session_id 重试，并设置 user_confirmed=true", map[string]any{
					"remote_session_id": remote.ID, "action": "run", "command": command,
					"purpose": purpose, "scope": scope, "user_confirmed": true,
				})
				return r.resultJSON(response)
			}
			result, executeErr := r.executeApprovedCommandTask(ctx, envReq, principal, remote, command, yield, purpose, scope, commandDigest, analysis)
			if executeErr == nil {
				if _, consumed := r.approvals.Consume(pending.ID); !consumed {
					return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "confirmation_state_error", "confirmed command approval could not be consumed")
				}
			}
			return result, executeErr
		}
		if !r.hasPendingCommandConfirmation(remote.ID, principal.ID, command, purpose, scope, confirmationToken) {
			pending, confirmationErr := r.approvals.PutPending(approval.Pending{
				Tool: "command_execute", Summary: command, Command: command,
				CommandYieldMs: int(yield / time.Millisecond), Purpose: purpose, Scope: scope,
				CommandDigest: commandDigest, WorkDir: remote.WorkspacePath,
				RequestID: envReq.RequestID, Workspace: remote.WorkspaceName,
				RemoteSessionID: remote.ID, PrincipalID: principal.ID,
			})
			if confirmationErr != nil {
				return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "confirmation_store_error", confirmationErr.Error())
			}
			// Struct field order puts confirmation_token first in the JSON
			// text, so host previews that truncate long tool output still show
			// the full token to the model.
			confirmationMessage := "confirmation_token: " + pending.ConfirmationToken + "；请向用户展示命令及用途，获得明确语义确认后，使用相同 command 和该 confirmation_token 重试。该 token 仅绑定本次操作，不承担认证职责。"
			if confirmationToken != "" {
				confirmationMessage = "你提供的 confirmation_token 未匹配当前待确认项；请使用本响应 data.confirmation_token 中的完整 token 原样重试：" + pending.ConfirmationToken + "（相同 command、session_id 和 scope）。"
			}
			confirmationData := commandConfirmationData{
				ConfirmationToken:    pending.ConfirmationToken,
				Command:              command,
				Purpose:              purpose,
				Scope:                scope,
				CommandDigest:        commandDigest,
				CommandPolicy:        commandPolicyData(analysis),
				ConfirmationRequired: true,
				ConfirmationMessage:  confirmationMessage,
			}
			response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, remote.WorkspaceName,
				confirmationData, "USER_CONFIRMATION_REQUIRED", "命令执行等待用户语义确认")
			response.RemoteSessionID = remote.ID
			return r.resultJSON(response)
		}
		result, executeErr := r.executeApprovedCommandTask(ctx, envReq, principal, remote, command, yield, purpose, scope, commandDigest, analysis)
		if executeErr == nil {
			r.consumePendingCommandConfirmation(remote.ID, principal.ID, command, purpose, scope, confirmationToken)
		}
		return result, executeErr
	}

	return r.executeApprovedCommandTask(ctx, envReq, principal, remote, command, commandYield(envReq.Payload), purpose, scope, commandDigest, analysis)
}

// commandConfirmationData keeps confirmation_token first in the serialized
// JSON payload so truncated host previews cannot hide it.
type commandConfirmationData struct {
	ConfirmationToken    string         `json:"confirmation_token"`
	Command              string         `json:"command"`
	Purpose              string         `json:"purpose"`
	Scope                string         `json:"scope"`
	CommandDigest        string         `json:"command_digest"`
	CommandPolicy        map[string]any `json:"command_policy,omitempty"`
	ConfirmationRequired bool           `json:"confirmation_required"`
	ConfirmationMessage  string         `json:"confirmation_message"`
}

func (r *Runtime) executeApprovedCommandTask(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session, command string, yield time.Duration, purpose, scope, commandDigest string, analysis security.CommandAnalysis) (*mcp.CallToolResult, error) {
	detail := commandExecutionDetail(purpose, scope, commandDigest, analysis)
	if err := r.writeAudit(audit.Event{
		RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName,
		Tool: "command_execute", Command: command, Status: "preflight_approved", Detail: detail,
	}); err != nil {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "audit_write_failed", "command preflight audit could not be persisted; no command segment was executed")
	}
	return r.executeCommandTask(ctx, envReq, principal, remote, command, yield, purpose, scope, commandDigest, analysis)
}

func (r *Runtime) executeCommandTask(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session, command string, yield time.Duration, purpose, scope, commandDigest string, analysis security.CommandAnalysis) (*mcp.CallToolResult, error) {
	originTool := toolInvocationName(ctx)
	if originTool == "" {
		originTool = "command_execute"
	}
	task, err := r.tasks.StartRemoteWithObservationContext(ctx, envReq.RequestID, observationCallID(envReq), originTool, remote.ID, remote.WorkspaceName, remote.WorkspacePath, command)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "start_error", err.Error())
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: remote.ID, Type: "command.started", OperationID: task.ID, Summary: command, Metadata: commandExecutionDetail(purpose, scope, commandDigest, analysis)})
	waitCtx, cancel := context.WithTimeout(ctx, yield)
	completed := task.Wait(waitCtx)
	cancel()
	data := r.taskResultData(task, 0, 0)
	data["purpose"] = purpose
	data["scope"] = scope
	data["command_digest"] = commandDigest
	data["command_policy"] = commandPolicyData(analysis)
	data["command"] = command
	data["working_directory"] = remote.WorkspacePath
	data["workspace_scoped"] = scope == "workspace"
	capTaskExecutionOutput(data, config.MaxResultBytes(r.cfg.Limits))
	if completed {
		data["completed_in_call"] = true
		delete(data, "execution_task_id")
		detail := commandExecutionDetail(purpose, scope, commandDigest, analysis)
		detail["exit_code"] = data["exit_code"]
		if exitCode, ok := data["exit_code"].(int); ok && exitCode != 0 {
			stderr, _ := data["stderr"].(string)
			code := commandFailureCode(exitCode, stderr)
			response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, data, code, commandFailureMessage(code, exitCode))
			response.RemoteSessionID = remote.ID
			return r.resultJSON(response)
		}
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "command_execute", Command: command, Status: "ok", Detail: detail})
		result := compactToolResult(data, commandOutputText(ctx, data, fmt.Sprintf("Command completed with exit code %v.", data["exit_code"])))
		return result, nil
	}
	data["completed_in_call"] = false
	nextTool := "task_manage"
	if isCleanCoreRequest(ctx) {
		nextTool = "execute"
	}
	data["next_action"] = nextAction(nextTool, map[string]any{
		"remote_session_id": remote.ID, "action": "attach", "execution_task_id": task.ID,
		"stdout_offset": data["stdout_next_offset"], "stderr_offset": data["stderr_next_offset"],
		"yield_time_ms": int(yield / time.Millisecond),
	})
	data["summary"] = fmt.Sprintf("Command is running as Task %s.", task.ID)
	detail := commandExecutionDetail(purpose, scope, commandDigest, analysis)
	detail["execution_task_id"] = task.ID
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "command_execute", Command: command, Status: "running", Detail: detail})
	response := envelope.Accepted(envReq.RequestID, remote.WorkspaceName, data)
	response.RemoteSessionID = remote.ID
	responseData, responseErr := r.resultJSON(response)
	if responseErr != nil {
		return nil, responseErr
	}
	return responseData, nil
}

// commandOutputText renders the model-facing summary with any stdout/stderr
// inline, so completed command output is readable directly in the conversation.
// Truncated streams state the truncation and point to task_read/task for more.
func commandOutputText(ctx context.Context, data map[string]any, summary string) string {
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
		builder.WriteString("\n\n")
		if isCleanCoreRequest(ctx) {
			builder.WriteString("Output truncated; call observe(view=logs) or execute(action=attach) to read more.")
		} else {
			builder.WriteString("Output truncated; call task(operation=attach) or task_read(view=logs) to read more.")
		}
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

func commandIntent(req envelope.Request) (purpose, scope string, err error) {
	purpose = strings.TrimSpace(req.Intent)
	if purpose == "" {
		return "", "", fmt.Errorf("purpose is required and must describe the user's requested development action")
	}
	if len(purpose) > 512 {
		return "", "", fmt.Errorf("purpose exceeds 512 bytes")
	}
	scope, _ = req.Payload["scope"].(string)
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "workspace"
	}
	if scope != "workspace" {
		return "", "", fmt.Errorf("unsupported execution scope %q; only workspace is allowed", scope)
	}
	return purpose, scope, nil
}

func commandFailureCode(exitCode int, stderr string) string {
	if exitCode == 127 {
		lower := strings.ToLower(stderr)
		if strings.Contains(lower, "command not found") || strings.Contains(lower, "no such file or directory") {
			return "COMMAND_NOT_FOUND"
		}
	}
	return "PROCESS_EXIT"
}

func commandFailureMessage(code string, exitCode int) string {
	if code == "COMMAND_NOT_FOUND" {
		return fmt.Sprintf("command executable was not found (exit code %d)", exitCode)
	}
	return fmt.Sprintf("command exited with code %d", exitCode)
}

func commandRequestDigest(requestID, remoteSessionID, workspace, command, purpose, scope string) string {
	// Request IDs identify transport attempts. They must not change the
	// semantic operation digest used to bind confirmation_token across retry.
	_ = requestID
	value := strings.Join([]string{remoteSessionID, workspace, command, purpose, scope}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func commandPolicyData(analysis security.CommandAnalysis) map[string]any {
	segments := make([]map[string]any, 0, len(analysis.Segments))
	for index, segment := range analysis.Segments {
		item := map[string]any{
			"index": index + 1, "command": segment.Command, "decision": segment.Decision.String(),
		}
		if segment.Operator != "" {
			item["operator_after"] = segment.Operator
		}
		segments = append(segments, item)
	}
	return map[string]any{
		"decision": analysis.Decision.String(), "segments": segments, "unsafe": analysis.Unsafe,
		"all_segments_preflighted": !analysis.Unsafe && len(segments) > 0,
		"atomic_policy_gate":       true,
		"execute_original_once":    true,
	}
}

func commandExecutionDetail(purpose, scope, commandDigest string, analysis security.CommandAnalysis) map[string]any {
	return map[string]any{
		"purpose": purpose, "scope": scope, "command_digest": commandDigest,
		"workspace_scoped": scope == "workspace", "command_policy": commandPolicyData(analysis),
	}
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

func (r *Runtime) hasPendingCommandConfirmation(remoteSessionID, principalID, command, purpose, scope, confirmationToken string) bool {
	confirmationToken = strings.TrimSpace(confirmationToken)
	if confirmationToken == "" {
		return false
	}
	for _, pending := range r.approvals.ListRemoteSession(remoteSessionID) {
		if pending.Tool == "command_execute" && pending.PrincipalID == principalID && pending.Command == command && pending.Scope == scope && pending.ConfirmationToken == confirmationToken {
			return true
		}
	}
	return false
}

func (r *Runtime) pendingCommandConfirmation(remoteSessionID, principalID, command, scope, digest string) (approval.Pending, bool) {
	for _, pending := range r.approvals.ListRemoteSession(remoteSessionID) {
		if pending.Tool == "command_execute" && pending.PrincipalID == principalID &&
			pending.Command == command && pending.Scope == scope && pending.CommandDigest == digest {
			return pending, true
		}
	}
	return approval.Pending{}, false
}

// cleanCommandConfirmationContentKey binds clean-core user confirmation to the
// exact semantic command digest. Legacy confirmation-token clients retain the
// broader command/scope dedup behavior in approval.contentKey.
func cleanCommandConfirmationContentKey(principalID, digest string) string {
	return strings.Join([]string{"clean-command", principalID, digest}, "\x00")
}

func (r *Runtime) consumePendingCommandConfirmation(remoteSessionID, principalID, command, purpose, scope, confirmationToken string) {
	for _, pending := range r.approvals.ListRemoteSession(remoteSessionID) {
		if pending.Tool == "command_execute" && pending.PrincipalID == principalID && pending.Command == command && pending.Scope == scope && pending.ConfirmationToken == confirmationToken {
			_, _ = r.approvals.Consume(pending.ID)
			return
		}
	}
}

// containsUnsafeShellFeature mirrors the security matcher's rejection reason
// so the model-facing error can explain why a compound verification command
// was denied instead of leaving the model to guess.
func containsUnsafeShellFeature(command string) bool {
	return security.HasUnsafeShellOperator(command)
}

func (r *Runtime) toolTaskManage(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	edit := action == "stop" || action == "stdin"
	envReq, principal, remote, fail := r.changeRequest(ctx, req, edit)
	if fail != nil {
		return fail, nil
	}
	if action == "list" {
		items, err := r.tasks.List(remote.ID, intPayload(envReq.Payload, "limit"))
		if err != nil {
			return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "task_list_error", err.Error())
		}
		digest := taskListDigest(items)
		knownDigest := strings.TrimSpace(stringPayload(envReq.Payload, "known_task_digest"))
		data := map[string]any{"task_list_digest": digest, "not_modified": knownDigest != "" && knownDigest == digest}
		if data["not_modified"] == true {
			data["tasks"] = []map[string]any{}
			data["message"] = "Task list unchanged; reuse the previously returned Task IDs."
		} else {
			data["tasks"] = items
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, data)
	}
	executionTaskID := strings.TrimSpace(stringPayload(envReq.Payload, "execution_task_id"))
	if executionTaskID == "" {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "EXECUTION_TASK_ID_REQUIRED", "execution_task_id is required")
	}
	if strings.HasPrefix(executionTaskID, "pt_") || strings.HasPrefix(executionTaskID, "pl_") {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "EXECUTION_TASK_ID_INVALID", "execution_task_id must identify a terminal execution Task; use observe(view=plan) for plan_task_id")
	}
	task, err := r.tasks.Get(remote.ID, executionTaskID)
	if err != nil {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "EXECUTION_TASK_NOT_FOUND", "execution_task_id does not belong to this Remote Session")
	}
	switch action {
	case "status":
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, task.StatusView())
	case "logs":
		data := r.taskResultData(task, intPayload(envReq.Payload, "stdout_offset"), intPayload(envReq.Payload, "stderr_offset"))
		if int64(data["stdout_next_offset"].(int)) < task.LogStreamSize("stdout") || int64(data["stderr_next_offset"].(int)) < task.LogStreamSize("stderr") {
			nextTool := "task_manage"
			if isCleanCoreRequest(ctx) {
				nextTool = "observe"
			}
			data["next_action"] = nextAction(nextTool, map[string]any{"remote_session_id": remote.ID, "view": "logs", "execution_task_id": task.ID, "stdout_offset": data["stdout_next_offset"], "stderr_offset": data["stderr_next_offset"]})
		}
		result := compactToolResult(data, commandOutputText(ctx, data, fmt.Sprintf("Task %s log chunk returned.", task.ID)))
		return result, nil
	case "attach":
		waitCtx, cancel := context.WithTimeout(ctx, commandYield(envReq.Payload))
		task.Wait(waitCtx)
		cancel()
		data := r.taskResultData(task, intPayload(envReq.Payload, "stdout_offset"), intPayload(envReq.Payload, "stderr_offset"))
		stdoutNext := data["stdout_next_offset"].(int)
		stderrNext := data["stderr_next_offset"].(int)
		if fmt.Sprint(task.StatusView()["status"]) == string(terminal.TaskRunning) || int64(stdoutNext) < task.LogStreamSize("stdout") || int64(stderrNext) < task.LogStreamSize("stderr") {
			nextTool := "task_manage"
			if isCleanCoreRequest(ctx) {
				nextTool = "execute"
			}
			data["next_action"] = nextAction(nextTool, map[string]any{"remote_session_id": remote.ID, "action": "attach", "execution_task_id": task.ID, "stdout_offset": stdoutNext, "stderr_offset": stderrNext, "yield_time_ms": int(commandYield(envReq.Payload) / time.Millisecond)})
		}
		result := compactToolResult(data, commandOutputText(ctx, data, fmt.Sprintf("Task %s attached.", task.ID)))
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
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"execution_task_id": task.ID, "pid": task.PID, "ports": ports})
	case "diagnostics":
		log, next := task.Logs(0)
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"execution_task_id": task.ID, "diagnostics": projecttask.ParseDiagnostics(log, intPayload(envReq.Payload, "limit")), "parsed_log_bytes": next})
	case "stdin":
		input, _ := envReq.Payload["input"].(string)
		if err := task.WriteStdin(input); err != nil {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "stdin_unavailable", err.Error())
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"execution_task_id": task.ID, "accepted_bytes": len(input)})
	default:
		return r.invalidAction(ctx, req, "task_manage", action)
	}
}

func (r *Runtime) taskResultData(task *terminal.Task, stdoutOffset, stderrOffset int) map[string]any {
	stdout, stdoutNext := task.LogsFor("stdout", stdoutOffset)
	stderr, stderrNext := task.LogsFor("stderr", stderrOffset)
	data := task.StatusView()
	data["execution_task_id"] = task.ID
	data["stdout"] = stdout
	data["stderr"] = stderr
	data["stdout_offset"] = stdoutOffset
	data["stderr_offset"] = stderrOffset
	data["stdout_next_offset"] = stdoutNext
	data["stderr_next_offset"] = stderrNext
	return data
}

func taskListDigest(items []map[string]any) string {
	stableItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		stable := make(map[string]any, 7)
		for _, key := range []string{"execution_task_id", "status", "pid", "command", "exit_code", "log_truncated", "finished_at"} {
			if value, ok := item[key]; ok {
				stable[key] = value
			}
		}
		stableItems = append(stableItems, stable)
	}
	encoded, _ := json.Marshal(stableItems)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
