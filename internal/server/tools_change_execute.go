package server

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/approval"
	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/changeset"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/instruction"
	"mcpx/internal/projecttask"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

// toolChangeExecute prepares and optionally applies a Changeset in one call (A10/A15).
func (r *Runtime) toolChangeExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	if preparedID, _ := envReq.Payload["changeset_id"].(string); strings.TrimSpace(preparedID) != "" {
		if _, hasOperations := envReq.Payload["operations"]; hasOperations {
			return r.changeError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("changeset_id cannot be combined with operations"))
		}
		return r.executePreparedChange(ctx, envReq, principal, session, strings.TrimSpace(preparedID))
	}
	if revertID, _ := envReq.Payload["revert_changeset_id"].(string); strings.TrimSpace(revertID) != "" {
		if _, hasOperations := envReq.Payload["operations"]; hasOperations {
			return r.changeError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("revert_changeset_id cannot be combined with operations"))
		}
		return r.executeRevertChange(ctx, envReq, principal, session, strings.TrimSpace(revertID))
	}
	clientRequestID, _ := envReq.Payload["idempotency_key"].(string)
	clientRequestID = strings.TrimSpace(clientRequestID)
	requestKey := ""
	if clientRequestID != "" {
		requestKey = session.ID + "\x00" + clientRequestID
		if item, found, findErr := r.changesets.FindIdempotent(ctx, session.ID, principal.ID, clientRequestID); findErr != nil {
			return r.changeError(envReq, session.ID, session.WorkspaceName, findErr)
		} else if found {
			return r.replayChangeExecute(envReq, session, item), nil
		}
		if changesetID, found := r.findChangeExecuteRequest(requestKey); found {
			item, getErr := r.changesets.Get(ctx, changesetID)
			if getErr != nil {
				return r.changeError(envReq, session.ID, session.WorkspaceName, getErr)
			}
			return r.replayChangeExecute(envReq, session, item), nil
		}
	}
	operations, err := parseChangeOperations(envReq.Payload["operations"])
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	effective := r.effectiveConfig(session.WorkspacePath)
	if effective.Security.Files.MaxPatchFiles > 0 && len(operations) > effective.Security.Files.MaxPatchFiles {
		return r.changeError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("patch has %d files; maximum is %d", len(operations), effective.Security.Files.MaxPatchFiles))
	}
	for _, operation := range operations {
		for _, path := range []string{operation.Path, operation.NewPath} {
			if path != "" && security.MatchFile(effective.Security.Files, path) == security.Deny {
				response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName, map[string]any{"path": path}, "FILE_DENIED", "file denied by policy")
				response.RemoteSessionID = session.ID
				return r.resultJSON(response)
			}
		}
	}
	instructionResolution, err := r.resolveChangeInstructions(session.WorkspacePath, operations)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	summary, _ := envReq.Payload["summary"].(string)
	apply := true
	if v, ok := envReq.Payload["apply"].(bool); ok {
		apply = v
	}
	format, _ := envReq.Payload["format"].(bool)
	var verify []string
	if raw, ok := envReq.Payload["verify"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && s != "" {
				verify = append(verify, s)
			}
		}
	}

	formatResults := make([]map[string]any, 0, len(operations))
	prepareOptions := changeset.PrepareOptions{}
	if format {
		prepareOptions.Transform = func(path string, content []byte) ([]byte, error) {
			if !strings.HasSuffix(path, ".go") {
				formatResults = append(formatResults, map[string]any{"path": path, "status": "skipped", "reason": "no_formatter"})
				return content, nil
			}
			formatted, formatErr := formatSourceContent(ctx, session.WorkspacePath, content)
			item := map[string]any{"path": path, "formatter": "gofmt"}
			if formatErr != nil {
				item["status"] = "error"
				item["error"] = formatErr.Error()
				formatResults = append(formatResults, item)
				return nil, formatErr
			}
			item["status"] = "ok"
			formatResults = append(formatResults, item)
			return formatted, nil
		}
	}
	var prepared changeset.Changeset
	var replayed bool
	if clientRequestID != "" {
		prepared, replayed, err = r.changesets.PrepareIdempotentWithOptions(ctx, session.ID, principal.ID, clientRequestID, session.WorkspacePath, summary, operations, prepareOptions)
	} else {
		prepared, err = r.changesets.PrepareWithOptions(ctx, session.ID, principal.ID, session.WorkspacePath, summary, operations, prepareOptions)
	}
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	if replayed {
		return r.replayChangeExecute(envReq, session, prepared), nil
	}
	if requestKey != "" {
		r.rememberChangeExecuteRequest(requestKey, prepared.ID)
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{
		RemoteSessionID: session.ID, Type: "changeset.prepared", OperationID: prepared.ID, Summary: prepared.Summary,
	})

	resultPayload := changeSummaryDTO(prepared)
	resultPayload["applied"] = false
	resultPayload["instruction_resolution"] = instructionResolution
	if format {
		resultPayload["format"] = formatResults
	}

	if !apply {
		out := changeDiffResultFromDTO(prepared, resultPayload)
		return out, nil
	}

	needsConfirmation, changedLines, deniedPath := evaluateChangesetPolicy(effective, prepared.Files)
	if deniedPath != "" {
		response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName, map[string]any{"path": deniedPath}, "FILE_DENIED", "file denied by policy")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	if effective.Security.Files.MaxPatchLines > 0 && changedLines > effective.Security.Files.MaxPatchLines {
		return r.patchTooLargeResult(envReq, session, changedLines, effective.Security.Files.MaxPatchLines)
	}
	confirmationToken := stringPayload(envReq.Payload, "confirmation_token")
	if needsConfirmation && !r.hasPendingChangeConfirmation(session.ID, principal.ID, prepared.ID, prepared.Digest, confirmationToken) {
		pending, confirmationErr := r.approvals.PutPending(approval.Pending{
			Tool: "change_execute", Summary: prepared.Summary, WorkDir: session.WorkspacePath, Workspace: session.WorkspaceName,
			RequestID: envReq.RequestID, RemoteSessionID: session.ID, PrincipalID: principal.ID,
			ChangesetID: prepared.ID, ChangesetDigest: prepared.Digest,
		})
		if confirmationErr != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "confirmation_store_error", confirmationErr.Error())
		}
		resultPayload["confirmation_required"] = true
		resultPayload["confirmation_token"] = pending.ConfirmationToken
		resultPayload["confirmation_message"] = "请向用户展示文件和差异，获得明确语义确认后，使用同一 changeset_id、expected_digest 和 confirmation_token 重试。该 token 仅绑定本次操作，不承担认证职责。"
		response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, session.WorkspaceName,
			resultPayload, "USER_CONFIRMATION_REQUIRED", "文件变更等待用户语义确认")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}

	applied, err := r.changesets.Apply(ctx, prepared.ID, session.WorkspacePath)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	appliedItem := prepared
	if current, getErr := r.changesets.Get(ctx, prepared.ID); getErr == nil {
		appliedItem = current
	}
	r.observeAppliedChangeset(ctx, envReq, session, appliedItem)
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{
		RemoteSessionID: session.ID, Type: "changeset.applied", OperationID: prepared.ID, Summary: prepared.Summary,
	})
	r.logAudit(audit.Event{
		RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName,
		Tool: "change_execute", Status: "ok", Detail: map[string]any{"changeset_id": prepared.ID, "digest": prepared.Digest},
	})

	resultPayload["applied"] = true
	resultPayload["apply"] = applied
	resultPayload["files_changed"] = applied.Files

	if len(verify) > 0 {
		verifyResults := r.runVerifySteps(ctx, envReq, session, verify)
		resultPayload["verify"] = verifyResults
	}

	return changeDiffResultFromDTO(prepared, resultPayload), nil
}

func (r *Runtime) patchTooLargeResult(envReq envelope.Request, session remotesession.Session, changedLines, maxLines int) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName,
		map[string]any{
			"lines": changedLines, "max_patch_lines": maxLines,
			"next_actions": []map[string]any{
				nextActionWithReason("change_manage", "split the patch into per-file Changesets before applying", map[string]any{"remote_session_id": session.ID, "action": "prepare"}),
				nextActionWithReason("change_execute", "retry with a smaller single-call operation set", map[string]any{"remote_session_id": session.ID}),
			},
		}, "patch_too_large", "Changeset exceeds max_patch_lines")
	response.RemoteSessionID = session.ID
	return r.resultJSON(response)
}

func (r *Runtime) executePreparedChange(ctx context.Context, envReq envelope.Request, principal auth.Principal, session remotesession.Session, changesetID string) (*mcp.CallToolResult, error) {
	item, err := r.changesets.Get(ctx, changesetID)
	if err != nil || item.RemoteSessionID != session.ID {
		if err == nil {
			err = changeset.ErrNotFound
		}
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	expectedDigest, _ := envReq.Payload["expected_digest"].(string)
	if strings.TrimSpace(expectedDigest) == "" || expectedDigest != item.Digest {
		path := ""
		if len(item.Files) > 0 {
			path = item.Files[0].Path
		}
		return r.changeError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("changeset digest mismatch for %s", path))
	}
	if apply, exists := envReq.Payload["apply"].(bool); exists && !apply {
		return changeDiffResult(item), nil
	}
	return r.applyPreparedChange(ctx, envReq, principal, session, item, stringPayload(envReq.Payload, "confirmation_token"))
}

func (r *Runtime) executeRevertChange(ctx context.Context, envReq envelope.Request, principal auth.Principal, session remotesession.Session, sourceID string) (*mcp.CallToolResult, error) {
	source, err := r.changesets.Get(ctx, sourceID)
	if err != nil || source.RemoteSessionID != session.ID {
		if err == nil {
			err = changeset.ErrNotFound
		}
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	revertOperations := make([]changeset.Operation, 0, len(source.Files))
	for _, item := range source.Files {
		revertOperations = append(revertOperations, changeset.Operation{Path: item.Path, NewPath: item.NewPath})
	}
	if _, err := r.resolveChangeInstructions(session.WorkspacePath, revertOperations); err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	revert, err := r.changesets.PrepareRevert(ctx, source.ID, principal.ID, session.WorkspacePath)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.revert_prepared", OperationID: revert.ID, Summary: revert.Summary})
	if apply, exists := envReq.Payload["apply"].(bool); exists && !apply {
		return changeDiffResult(revert), nil
	}
	// A public change_revert is bound to the source Changeset ID. The generated
	// revert draft is intentionally recreated on retry, so binding the semantic
	// confirmation to the generated draft would make the returned token unusable.
	effective := r.effectiveConfig(session.WorkspacePath)
	needsConfirmation, changedLines, deniedPath := evaluateChangesetPolicy(effective, revert.Files)
	if deniedPath != "" {
		response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName, map[string]any{"path": deniedPath}, "FILE_DENIED", "file denied by policy")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	if effective.Security.Files.MaxPatchLines > 0 && changedLines > effective.Security.Files.MaxPatchLines {
		return r.patchTooLargeResult(envReq, session, changedLines, effective.Security.Files.MaxPatchLines)
	}
	confirmationToken := stringPayload(envReq.Payload, "confirmation_token")
	if needsConfirmation && !r.hasPendingChangeConfirmation(session.ID, principal.ID, source.ID, source.Digest, confirmationToken) {
		pending, confirmationErr := r.approvals.PutPending(approval.Pending{
			Tool: "change_execute", Summary: revert.Summary, WorkDir: session.WorkspacePath, Workspace: session.WorkspaceName,
			RequestID: envReq.RequestID, RemoteSessionID: session.ID, PrincipalID: principal.ID,
			ChangesetID: source.ID, ChangesetDigest: source.Digest,
		})
		if confirmationErr != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "confirmation_store_error", confirmationErr.Error())
		}
		confirmationData := changeSummaryDTO(revert)
		confirmationData["changeset_id"] = source.ID
		confirmationData["expected_digest"] = source.Digest
		confirmationData["revert_changeset_id"] = revert.ID
		confirmationData["confirmation_required"] = true
		confirmationData["confirmation_token"] = pending.ConfirmationToken
		confirmationData["confirmation_message"] = "请向用户展示回滚差异，获得明确语义确认后，使用同一 changeset_id 和 confirmation_token 重试。该 token 仅绑定本次回滚，不承担认证职责。"
		response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, session.WorkspaceName,
			confirmationData, "USER_CONFIRMATION_REQUIRED", "文件回滚等待用户语义确认")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	result, err := r.applyChangeset(ctx, envReq, principal.ID, session, revert)
	if err == nil && confirmationToken != "" {
		r.consumePendingChangeConfirmation(session.ID, principal.ID, source.ID, source.Digest, confirmationToken)
	}
	return result, err
}

func (r *Runtime) applyPreparedChange(ctx context.Context, envReq envelope.Request, principal auth.Principal, session remotesession.Session, item changeset.Changeset, confirmationToken string) (*mcp.CallToolResult, error) {
	effective := r.effectiveConfig(session.WorkspacePath)
	needsConfirmation, changedLines, deniedPath := evaluateChangesetPolicy(effective, item.Files)
	if deniedPath != "" {
		response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName, map[string]any{"path": deniedPath}, "FILE_DENIED", "file denied by policy")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	if effective.Security.Files.MaxPatchLines > 0 && changedLines > effective.Security.Files.MaxPatchLines {
		return r.patchTooLargeResult(envReq, session, changedLines, effective.Security.Files.MaxPatchLines)
	}
	if needsConfirmation && !r.hasPendingChangeConfirmation(session.ID, principal.ID, item.ID, item.Digest, confirmationToken) {
		pending, confirmationErr := r.approvals.PutPending(approval.Pending{
			Tool: "change_execute", Summary: item.Summary, WorkDir: session.WorkspacePath, Workspace: session.WorkspaceName,
			RequestID: envReq.RequestID, RemoteSessionID: session.ID, PrincipalID: principal.ID,
			ChangesetID: item.ID, ChangesetDigest: item.Digest,
		})
		if confirmationErr != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "confirmation_store_error", confirmationErr.Error())
		}
		confirmationData := changeSummaryDTO(item)
		confirmationData["confirmation_required"] = true
		confirmationData["confirmation_token"] = pending.ConfirmationToken
		confirmationData["confirmation_message"] = "请向用户展示文件和差异，获得明确语义确认后，使用同一 changeset_id、expected_digest 和 confirmation_token 重试。该 token 仅绑定本次操作，不承担认证职责。"
		response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, session.WorkspaceName,
			confirmationData, "USER_CONFIRMATION_REQUIRED", "文件变更等待用户语义确认")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	result, err := r.applyChangeset(ctx, envReq, principal.ID, session, item)
	if err == nil && confirmationToken != "" {
		r.consumePendingChangeConfirmation(session.ID, principal.ID, item.ID, item.Digest, confirmationToken)
	}
	return result, err
}

func (r *Runtime) replayChangeExecute(envReq envelope.Request, session remotesession.Session, item changeset.Changeset) *mcp.CallToolResult {
	payload := changeSummaryDTO(item)
	payload["applied"] = item.Status == "applied"
	payload["idempotent_replay"] = true
	if item.Status == "draft" {
		for _, pending := range r.approvals.ListRemoteSession(session.ID) {
			if pending.ChangesetID != item.ID || pending.Tool != "change_execute" {
				continue
			}
			response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, session.WorkspaceName,
				map[string]any{
					"changeset_id": item.ID, "digest": item.Digest, "diff": payload["diff"],
					"confirmation_required": true,
					"confirmation_token":    pending.ConfirmationToken,
					"confirmation_message":  "请向用户展示文件和差异，获得明确语义确认后，使用同一 changeset_id、expected_digest 和 confirmation_token 重试。",
				}, "USER_CONFIRMATION_REQUIRED", "文件变更等待用户语义确认")
			response.RemoteSessionID = session.ID
			result, _ := r.resultJSON(response)
			return result
		}
	}
	return changeDiffResultFromDTO(item, payload)
}

func (r *Runtime) hasPendingChangeConfirmation(remoteSessionID, principalID, changesetID, digest, confirmationToken string) bool {
	confirmationToken = strings.TrimSpace(confirmationToken)
	if confirmationToken == "" {
		return false
	}
	for _, pending := range r.approvals.ListRemoteSession(remoteSessionID) {
		if pending.Tool == "change_execute" &&
			pending.PrincipalID == principalID && pending.ChangesetID == changesetID && pending.ChangesetDigest == digest && pending.ConfirmationToken == confirmationToken {
			return true
		}
	}
	return false
}

func (r *Runtime) consumePendingChangeConfirmation(remoteSessionID, principalID, changesetID, digest, confirmationToken string) {
	for _, pending := range r.approvals.ListRemoteSession(remoteSessionID) {
		if pending.Tool == "change_execute" &&
			pending.PrincipalID == principalID && pending.ChangesetID == changesetID && pending.ChangesetDigest == digest && pending.ConfirmationToken == confirmationToken {
			_, _ = r.approvals.Consume(pending.ID)
			return
		}
	}
}

func formatSourceContent(ctx context.Context, workspacePath string, content []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gofmt")
	cmd.Dir = workspacePath
	cmd.Stdin = bytes.NewReader(content)
	return cmd.Output()
}

func (r *Runtime) resolveChangeInstructions(workspacePath string, operations []changeset.Operation) (map[string]any, error) {
	paths := make([]string, 0, len(operations)*2)
	seen := make(map[string]struct{}, len(operations)*2)
	for _, operation := range operations {
		for _, path := range []string{operation.Path, operation.NewPath} {
			if path == "" {
				continue
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	resolution := instruction.ResolveForPaths(
		r.cfg.Discovery.Instructions.GlobalAgentsPath,
		workspacePath,
		paths,
		r.effectiveConfig(workspacePath).Security.Files.MaxReadBytes,
	)
	conflicts, _ := resolution["conflicts"].([]map[string]any)
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("instruction conflict: %v", conflicts[0])
	}
	return resolution, nil
}

func evaluateChangesetPolicy(effective config.Config, files []changeset.FileChange) (needsConfirmation bool, changedLines int, deniedPath string) {
	for _, file := range files {
		changedLines += changedLineCount(file.Original, file.Proposed)
		if changeset.IsDirectoryChange(file) {
			changedLines++
		}
		for _, path := range []string{file.Path, file.NewPath} {
			if path == "" {
				continue
			}
			switch security.MatchFile(effective.Security.Files, path) {
			case security.Deny:
				return false, changedLines, path
			case security.Confirm:
				needsConfirmation = true
			}
			if file.Operation == "delete" {
				needsConfirmation = true
			}
		}
	}
	return needsConfirmation, changedLines, ""
}

// changedLineCount estimates how many lines actually differ between the
// original and the proposed content. It trims the common prefix and suffix
// and counts the remaining middle as changed, so a small edit inside a large
// file counts as a few lines instead of the whole file twice (which rejected
// legitimate single-field edits on files larger than max_patch_lines/2).
func changedLineCount(original, proposed []byte) int {
	a := strings.Split(string(original), "\n")
	b := strings.Split(string(proposed), "\n")
	if len(a) > 0 && a[len(a)-1] == "" {
		a = a[:len(a)-1]
	}
	if len(b) > 0 && b[len(b)-1] == "" {
		b = b[:len(b)-1]
	}
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffixA, suffixB := len(a), len(b)
	for suffixA > prefix && suffixB > prefix && a[suffixA-1] == b[suffixB-1] {
		suffixA--
		suffixB--
	}
	return (suffixA - prefix) + (suffixB - prefix)
}

func (r *Runtime) runVerifySteps(ctx context.Context, envReq envelope.Request, session remotesession.Session, steps []string) []map[string]any {
	results := make([]map[string]any, 0, len(steps))
	discovered := projecttask.Discover(session.WorkspacePath)
	findTask := func(candidates ...string) (projecttask.Task, bool) {
		for _, candidate := range candidates {
			for _, task := range discovered {
				if strings.EqualFold(task.Name, candidate) || strings.Contains(strings.ToLower(task.Name), strings.ToLower(candidate)) {
					return task, true
				}
			}
		}
		return projecttask.Task{}, false
	}
	for _, step := range steps {
		start := time.Now()
		item := map[string]any{"step": step}
		if strings.EqualFold(step, "format") {
			item["status"] = "ok"
			item["note"] = "formatting was applied inside the Changeset"
			item["duration_ms"] = time.Since(start).Milliseconds()
			results = append(results, item)
			continue
		}
		candidates := []string{step}
		if strings.EqualFold(step, "typecheck") || strings.EqualFold(step, "lint") || strings.EqualFold(step, "test") || strings.EqualFold(step, "related_tests") {
			candidates = []string{step, "test", "check", "lint", "typecheck", "build"}
		}
		project, ok := findTask(candidates...)
		if !ok {
			item["status"] = "skipped"
			item["reason"] = "task_not_found"
			item["duration_ms"] = time.Since(start).Milliseconds()
			results = append(results, item)
			continue
		}
		item["command"] = project.Command
		item["purpose"] = fmt.Sprintf("Automatically verify the applied change with project task %s", step)
		item["scope"] = "workspace"
		item["command_digest"] = commandRequestDigest("verify:"+step, session.ID, session.WorkspaceName, project.Command, item["purpose"].(string), "workspace")
		decision := security.MatchCommand(r.effectiveConfig(session.WorkspacePath).Security.Commands, project.Command)
		if decision == security.Deny {
			item["status"] = "denied"
			item["reason"] = "command_policy"
			item["duration_ms"] = time.Since(start).Milliseconds()
			results = append(results, item)
			continue
		}
		task, err := r.tasks.StartRemoteWithObservation(ctx, envReq.RequestID, "change_execute", session.ID, session.WorkspaceName, session.WorkspacePath, project.Command)
		if err != nil {
			item["status"] = "failed"
			item["error"] = err.Error()
			item["duration_ms"] = time.Since(start).Milliseconds()
			results = append(results, item)
			continue
		}
		waitCtx, cancel := context.WithTimeout(ctx, defaultCommandYield)
		completed := task.Wait(waitCtx)
		cancel()
		view := task.StatusView()
		item["task_id"] = task.ID
		item["status"] = view["status"]
		item["exit_code"] = view["exit_code"]
		item["duration_ms"] = view["runtime_ms"]
		item["log_resource_uri"] = fmt.Sprintf("mcpx://remote-sessions/%s/tasks/%s/logs", session.ID, task.ID)
		if completed {
			if code, ok := view["exit_code"].(int); ok && code != 0 {
				item["status"] = "failed"
			}
		} else {
			item["next_action"] = nextAction("task_manage", map[string]any{
				"remote_session_id": session.ID, "action": "attach", "task_id": task.ID,
				"stdout_offset": 0, "stderr_offset": 0, "yield_time_ms": int(defaultCommandYield / time.Millisecond),
			})
		}
		results = append(results, item)
	}
	return results
}
