package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/approval"
	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/plan"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
	"mcpx/internal/terminal"
)

const (
	defaultPiYieldMs    = 30_000
	maxPiYieldMs        = 300_000
	defaultPiTimeoutSec = 600
	maxPiTimeoutSec     = 3600
	maxPiPromptBytes    = 16_384
)

// piEntryPattern matches the node entry path inside npm shims (pi / pi.cmd).
var piEntryPattern = regexp.MustCompile(`(?i)node_modules[\\/][^"\r\n]+?\.js`)

func piYield(payload map[string]any) time.Duration {
	yield := intPayload(payload, "yield_time_ms")
	if yield <= 0 {
		return defaultPiYieldMs * time.Millisecond
	}
	if yield > maxPiYieldMs {
		yield = maxPiYieldMs
	}
	return time.Duration(yield) * time.Millisecond
}

func piTimeout(payload map[string]any) time.Duration {
	secs := intPayload(payload, "timeout_seconds")
	if secs <= 0 {
		secs = defaultPiTimeoutSec
	}
	if secs > maxPiTimeoutSec {
		secs = maxPiTimeoutSec
	}
	return time.Duration(secs) * time.Second
}

// piDisplayCommand renders the policy-checkable, auditable command line for a
// dispatch. It is never executed directly; execution always uses the resolved
// launcher so the prompt cannot reach a shell parser.
func piDisplayCommand(prompt, model, system string) string {
	var b strings.Builder
	b.WriteString("pi -p ")
	b.WriteString(strconv.Quote(truncateRunes(prompt, 120)))
	if model != "" {
		b.WriteString(" --model ")
		b.WriteString(strconv.Quote(model))
	}
	if system != "" {
		b.WriteString(" --append-system-prompt ")
		b.WriteString(strconv.Quote(truncateRunes(system, 80)))
	}
	b.WriteString(" --no-session --approve")
	return b.String()
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// resolvePiLauncher returns the executable and full argument vector used to
// invoke the local Pi agent without shell parsing. npm shims (pi.cmd on
// Windows, pi on Unix) are resolved to the underlying node CLI entry so the
// prompt is never interpolated through a shell.
func resolvePiLauncher(prompt, model, system string) (string, []string, error) {
	shim, err := exec.LookPath("pi")
	if err != nil {
		return "", nil, fmt.Errorf("pi agent not found on PATH: %w", err)
	}
	exe := ""
	args := []string{}
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(shim)
		if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
			raw, readErr := os.ReadFile(shim)
			if readErr != nil {
				return "", nil, fmt.Errorf("read pi shim: %w", readErr)
			}
			match := piEntryPattern.Find(raw)
			if match == nil {
				return "", nil, fmt.Errorf("pi shim %s does not contain a node entry", shim)
			}
			entry := filepath.Join(filepath.Dir(shim), filepath.FromSlash(string(match)))
			nodePath, nodeErr := exec.LookPath("node")
			if nodeErr != nil {
				return "", nil, fmt.Errorf("node not found on PATH: %w", nodeErr)
			}
			exe = nodePath
			args = append(args, entry)
		} else {
			exe = shim
		}
	} else {
		if raw, readErr := os.ReadFile(shim); readErr == nil && strings.HasPrefix(string(raw), "#!") {
			line := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(string(raw), "\n", 2)[0], "#!"))
			if parts := strings.Fields(line); len(parts) > 0 {
				exe = parts[0]
				args = append(args, parts[1:]...)
			}
		}
		if exe == "" {
			exe = shim
		}
	}
	args = append(args, "-p", prompt, "--no-session", "--approve")
	if model != "" {
		args = append(args, "--model", model)
	}
	if system != "" {
		args = append(args, "--append-system-prompt", system)
	}
	return exe, args, nil
}

// toolPiExecute dispatches a task to the local Pi agent. When plan_id and
// plan_task_id are provided the plan task is started first and closed with
// execute evidence (complete on success, block on failure) after the process
// finishes inline. Long runs continue as a persistent execution Task that
// clients can attach to with execute(action=attach).
func (r *Runtime) toolPiExecute(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	purpose, scope, intentErr := commandIntent(envReq)
	if intentErr != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", intentErr.Error())
	}
	prompt := strings.TrimSpace(stringPayload(envReq.Payload, "prompt"))
	if prompt == "" {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "prompt is required")
	}
	if len(prompt) > maxPiPromptBytes {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", fmt.Sprintf("prompt exceeds %d bytes", maxPiPromptBytes))
	}
	planID := strings.TrimSpace(stringPayload(envReq.Payload, "plan_id"))
	planTaskID := strings.TrimSpace(stringPayload(envReq.Payload, "plan_task_id"))
	if (planID == "") != (planTaskID == "") {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "plan_id and plan_task_id must be provided together")
	}
	model := strings.TrimSpace(stringPayload(envReq.Payload, "model"))
	system := stringPayload(envReq.Payload, "system")

	// Plan task: start (or reuse an already started) task before dispatching.
	if planTaskID != "" {
		started, err := r.ensurePlanTaskInProgress(ctx, remote.ID, principal.ID, planID, planTaskID)
		if err != nil {
			return r.planError(envReq, remote, err)
		}
		_ = started
	}

	displayCommand := piDisplayCommand(prompt, model, system)
	effective := r.effectiveConfig(remote.WorkspacePath)
	if !effective.Terminal.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "terminal tools are disabled")
	}
	analysis := security.AnalyzeCommand(effective.Security.Commands, displayCommand)
	commandDigest := commandRequestDigest(envReq.RequestID, remote.ID, remote.WorkspaceName, displayCommand, purpose, scope)
	yield := piYield(envReq.Payload)

	executeApproved := func() (*mcp.CallToolResult, error) {
		return r.runPiTask(ctx, envReq, principal, remote, prompt, model, system, displayCommand, commandDigest, purpose, scope, analysis, yield, planID, planTaskID)
	}
	switch analysis.Decision {
	case security.Deny:
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "pi_execute", Command: displayCommand, Status: "denied", Detail: map[string]any{"plan_task_id": planTaskID}})
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "denied", "pi dispatch denied by command policy")
	case security.Confirm:
		userConfirmed := boolPayload(envReq.Payload, "user_confirmed")
		pending, pendingOK := r.pendingCommandConfirmation(remote.ID, principal.ID, displayCommand, scope, commandDigest)
		if !userConfirmed || !pendingOK {
			if !pendingOK {
				var confirmationErr error
				pending, confirmationErr = r.approvals.PutPending(approval.Pending{
					Tool: "pi_execute", Summary: displayCommand, Command: displayCommand,
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
				"command": displayCommand, "purpose": purpose, "scope": scope,
				"command_digest": commandDigest, "pending_digest": commandDigest,
				"command_policy":        commandPolicyData(analysis),
				"confirmation_required": true, "user_confirmed_required": true,
				"summary": "执行已完成策略预检；请向用户展示 Pi 委派摘要及用途，确认后将 user_confirmed=true 原样重试。",
			}
			response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, remote.WorkspaceName,
				confirmationData, "USER_CONFIRMATION_REQUIRED", "Pi 任务委派等待用户语义确认")
			response.RemoteSessionID = remote.ID
			addRecoveryAction(&response, "pi_execute", "用户确认后使用相同 prompt、purpose 和 remote_session_id 重试，并设置 user_confirmed=true", map[string]any{
				"remote_session_id": remote.ID, "prompt": prompt, "purpose": purpose,
				"plan_id": planID, "plan_task_id": planTaskID, "user_confirmed": true,
			})
			return r.resultJSON(response)
		}
		result, executeErr := executeApproved()
		if executeErr == nil {
			if _, consumed := r.approvals.Consume(pending.ID); !consumed {
				return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "confirmation_state_error", "confirmed pi approval could not be consumed")
			}
		}
		return result, executeErr
	default:
		return executeApproved()
	}
}

func (r *Runtime) runPiTask(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session,
	prompt, model, system, displayCommand, commandDigest, purpose, scope string, analysis security.CommandAnalysis,
	yield time.Duration, planID, planTaskID string) (*mcp.CallToolResult, error) {
	exe, args, err := resolvePiLauncher(prompt, model, system)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "pi_unavailable", err.Error())
	}
	task, err := r.tasks.StartRemoteProcessWithObservationContext(
		envReq.RequestID, observationCallID(envReq), "pi_execute", remote.ID, remote.WorkspaceName,
		remote.WorkspacePath, displayCommand,
		terminal.ProcessSpec{Executable: exe, Args: args, WallLimit: piTimeout(envReq.Payload)},
	)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "start_error", err.Error())
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{
		RemoteSessionID: remote.ID, Type: "pi_execute.started", OperationID: task.ID,
		Summary: truncateRunes(prompt, 200),
		Metadata: map[string]any{
			"plan_id": planID, "plan_task_id": planTaskID, "command_digest": commandDigest,
		},
	})
	waitCtx, cancel := context.WithTimeout(ctx, yield)
	completed := task.Wait(waitCtx)
	cancel()
	data := r.taskResultData(task, 0, 0)
	data["purpose"] = purpose
	data["scope"] = scope
	data["command_digest"] = commandDigest
	data["command_policy"] = commandPolicyData(analysis)
	data["command"] = displayCommand
	data["prompt"] = truncateRunes(prompt, 400)
	data["working_directory"] = remote.WorkspacePath
	if model != "" {
		data["pi_model"] = model
	}
	if system != "" {
		data["pi_system_injected"] = true
	}
	capTaskExecutionOutput(data, config.MaxResultBytes(r.cfg.Limits))
	if completed {
		data["completed_in_call"] = true
		delete(data, "execution_task_id")
		if planTaskID != "" {
			closed, closeErr := r.closePlanTaskAfterPi(ctx, remote.ID, principal.ID, planID, planTaskID, task.ID, data)
			if closeErr != nil {
				return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "plan_close_error", closeErr.Error())
			}
			data["plan_task"] = planTaskMap(planID, *closed)
		}
		if code, message := annotateExecutionOutcome(data); code != "" {
			response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, data, code, message)
			response.RemoteSessionID = remote.ID
			return r.resultJSON(response)
		}
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "pi_execute", Command: displayCommand, Status: "ok", Detail: map[string]any{"plan_task_id": planTaskID, "exit_code": data["exit_code"]}})
		return compactToolResult(data, fmt.Sprintf("Pi agent completed with exit code %v.", data["exit_code"])), nil
	}
	data["completed_in_call"] = false
	data["next_action"] = nextAction("execute", map[string]any{
		"remote_session_id": remote.ID, "action": "attach", "execution_task_id": task.ID,
		"stdout_offset": data["stdout_next_offset"], "stderr_offset": data["stderr_next_offset"],
		"yield_time_ms": int(yield / time.Millisecond),
	})
	data["summary"] = fmt.Sprintf("Pi agent is running as Task %s.", task.ID)
	return compactToolResult(data, data["summary"].(string)), nil
}

// ensurePlanTaskInProgress starts a todo/blocked plan task; an already
// in_progress task is reused without error.
func (r *Runtime) ensurePlanTaskInProgress(ctx context.Context, remoteSessionID, principalID, planID, taskID string) (*plan.Task, error) {
	item, err := r.plans.Get(ctx, remoteSessionID, planID)
	if err != nil {
		return nil, err
	}
	var target *plan.Task
	for i := range item.Tasks {
		if item.Tasks[i].ID == taskID {
			target = &item.Tasks[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: plan task %s", plan.ErrNotFound, taskID)
	}
	switch target.Status {
	case plan.TaskTodo, plan.TaskBlocked:
		t, err := r.plans.StartTask(ctx, remoteSessionID, planID, taskID, principalID)
		if err != nil {
			return nil, err
		}
		return &t, nil
	case plan.TaskInProgress:
		return target, nil
	default:
		return nil, fmt.Errorf("%w: task %s is %s", plan.ErrInvalidState, taskID, target.Status)
	}
}

// closePlanTaskAfterPi closes the plan task with execute evidence: completed
// on exit code 0, blocked otherwise.
func (r *Runtime) closePlanTaskAfterPi(ctx context.Context, remoteSessionID, principalID, planID, planTaskID, executionTaskID string, data map[string]any) (*plan.Task, error) {
	evidence := []plan.EvidenceInput{{Kind: plan.EvidenceExecute, ReferenceID: executionTaskID}}
	if code, ok := data["error_code"].(string); ok && code != "" {
		reason := fmt.Sprintf("pi dispatch failed: %s (execution task %s)", code, executionTaskID)
		t, err := r.plans.BlockTask(ctx, remoteSessionID, planID, planTaskID, principalID, reason, evidence)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}
	exitCode, _ := data["exit_code"].(int)
	if exitCode != 0 {
		stderr := strings.TrimSpace(fmt.Sprint(data["stderr"]))
		if stderr != "" {
			stderr = truncateRunes(stderr, 300)
		}
		reason := fmt.Sprintf("pi agent exited with code %d", exitCode)
		if stderr != "" {
			reason += ": " + stderr
		}
		t, err := r.plans.BlockTask(ctx, remoteSessionID, planID, planTaskID, principalID, reason, evidence)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}
	evidence[0].Metadata = map[string]any{"exit_code": 0, "tool": "pi_execute"}
	t, err := r.plans.CompleteTask(ctx, remoteSessionID, planID, planTaskID, principalID, evidence)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
