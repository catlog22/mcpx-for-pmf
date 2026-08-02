package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/approval"
	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/changeset"
	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

func (r *Runtime) toolChangePrepare(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
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
	if _, err := r.resolveChangeInstructions(session.WorkspacePath, operations); err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	summary, _ := envReq.Payload["summary"].(string)
	prepared, err := r.changesets.Prepare(ctx, session.ID, principal.ID, session.WorkspacePath, summary, operations)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.prepared", OperationID: prepared.ID, Summary: prepared.Summary})
	return changeDiffResult(prepared), nil
}

func (r *Runtime) toolChangeDiff(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	changesetID, _ := envReq.Payload["changeset_id"].(string)
	item, err := r.changesets.Get(ctx, changesetID)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	if item.RemoteSessionID != session.ID {
		return r.changeError(envReq, session.ID, session.WorkspaceName, changeset.ErrNotFound)
	}
	return changeDiffResult(item), nil
}

func (r *Runtime) toolChangeApply(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	changesetID, _ := envReq.Payload["changeset_id"].(string)
	item, err := r.changesets.Get(ctx, changesetID)
	if err != nil || item.RemoteSessionID != session.ID {
		if err == nil {
			err = changeset.ErrNotFound
		}
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	expectedDigest, _ := envReq.Payload["expected_digest"].(string)
	if expectedDigest == "" || expectedDigest != item.Digest {
		return r.changeError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("changeset digest mismatch"))
	}
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
	if needsConfirmation {
		approvalID, approvalErr := r.approvals.Put(approval.Pending{
			Tool: "change_apply", Summary: item.Summary, WorkDir: session.WorkspacePath, Workspace: session.WorkspaceName,
			RequestID: envReq.RequestID, RemoteSessionID: session.ID, PrincipalID: principal.ID,
			ChangesetID: item.ID, ChangesetDigest: item.Digest,
		})
		if approvalErr != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "approval_store_error", approvalErr.Error())
		}
		approvalData := changeSummaryDTO(item)
		approvalData["approval_id"] = approvalID
		approvalData["next_action"] = nextAction("approval_manage", map[string]any{"remote_session_id": session.ID, "action": "approve", "approval_id": approvalID})
		response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, session.WorkspaceName,
			approvalData, "APPROVAL_REQUIRED", "Changeset apply requires approval")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	return r.applyChangeset(ctx, envReq, principal.ID, session, item)
}

func (r *Runtime) toolChangeHistory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	history, err := r.changesets.History(ctx, session.ID, intPayload(envReq.Payload, "limit"))
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{"changesets": history})
}

func (r *Runtime) toolChangeRevert(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	changesetID, _ := envReq.Payload["changeset_id"].(string)
	source, err := r.changesets.Get(ctx, changesetID)
	if err != nil || source.RemoteSessionID != session.ID {
		if err == nil {
			err = changeset.ErrNotFound
		}
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	revertOperations := make([]changeset.Operation, 0, len(source.Files))
	for _, file := range source.Files {
		revertOperations = append(revertOperations, changeset.Operation{Path: file.Path, NewPath: file.NewPath})
	}
	if _, err := r.resolveChangeInstructions(session.WorkspacePath, revertOperations); err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	revert, err := r.changesets.PrepareRevert(ctx, source.ID, principal.ID, session.WorkspacePath)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.revert_prepared", OperationID: revert.ID, Summary: revert.Summary})
	return changeDiffResult(revert), nil
}

func (r *Runtime) changeRequest(ctx context.Context, req mcp.CallToolRequest, edit bool) (envelope.Request, auth.Principal, remotesession.Session, *mcp.CallToolResult) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return envReq, principal, remotesession.Session{}, fail
	}
	remoteSessionID, err := requireRemoteSessionID(envReq)
	if err != nil {
		result, _ := r.remoteError(envReq, "", "", err)
		return envReq, principal, remotesession.Session{}, result
	}
	session, err := r.remote.Get(ctx, principal, remoteSessionID)
	if err != nil {
		result, _ := r.remoteError(envReq, remoteSessionID, "", err)
		return envReq, principal, remotesession.Session{}, result
	}
	if edit && session.Role != "owner" && session.Role != "editor" {
		result, _ := r.remoteError(envReq, remoteSessionID, session.WorkspaceName, remotesession.ErrForbidden)
		return envReq, principal, remotesession.Session{}, result
	}
	return envReq, principal, session, nil
}

func (r *Runtime) applyChangeset(ctx context.Context, envReq envelope.Request, principalID string, session remotesession.Session, item changeset.Changeset) (*mcp.CallToolResult, error) {
	result, err := r.changesets.Apply(ctx, item.ID, session.WorkspacePath)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	principal, err := r.principalFromContext(ctx)
	if err == nil && principal.ID == principalID {
		_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.applied", OperationID: item.ID, Summary: item.Summary})
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "change_apply", Status: "ok", Detail: map[string]any{"changeset_id": item.ID, "digest": item.Digest}})
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, result)
}

func parseChangeOperations(value any) ([]changeset.Operation, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var operations []changeset.Operation
	if err := json.Unmarshal(encoded, &operations); err != nil {
		return nil, fmt.Errorf("operations must be an array of objects: %w", err)
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("operations are required")
	}
	return operations, nil
}

const (
	diffInlineMaxBytes       = 256 << 10 // 256 KiB inline budget
	diffPreviewLines         = 100
	diffFilePreviewMaxBytes  = 32 << 10 // 32 KiB per-file UI preview budget
	diffFilesPreviewMaxBytes = 64 << 10 // 64 KiB aggregate per-file UI preview budget
)

func changeSummaryDTO(item changeset.Changeset) map[string]any {
	files := make([]map[string]any, 0, len(item.Files))
	previewBudget := diffFilesPreviewMaxBytes
	for _, f := range item.Files {
		file := map[string]any{
			"path": f.Path, "new_path": f.NewPath, "operation": f.Operation,
			"original_sha256": f.OriginalSHA256, "proposed_sha256": f.ProposedSHA256,
			"expected_sha256": f.ExpectedSHA256,
		}
		if previewBudget > 0 {
			fileBudget := diffFilePreviewMaxBytes
			if previewBudget < fileBudget {
				fileBudget = previewBudget
			}
			if preview, truncated := fileDiffPreview(f, fileBudget); preview != "" {
				file["diff"] = preview
				previewBudget -= len(preview)
				if truncated {
					file["diff_truncated"] = true
				}
			}
		}
		files = append(files, file)
	}
	diffBytes := len(item.UnifiedDiff)
	uri := fmt.Sprintf("mcpx://remote-sessions/%s/changesets/%s/diff", item.RemoteSessionID, item.ID)
	diff := map[string]any{
		"bytes": diffBytes, "resource_uri": uri, "preview_lines": diffPreviewLines,
	}
	if diffBytes <= diffInlineMaxBytes {
		diff["mode"] = "inline"
		diff["unified_diff"] = item.UnifiedDiff
	} else {
		diff["mode"] = "resource"
		// Preview only — never embed full large diff in structured/text.
		diff["unified_diff_preview"] = trimDiffPreview(item.UnifiedDiff, diffPreviewLines)
	}
	return map[string]any{
		"changeset_id": item.ID, "remote_session_id": item.RemoteSessionID,
		"status": item.Status, "summary": item.Summary, "digest": item.Digest,
		"files": files, "diff": diff, "created_at": item.CreatedAt,
		"source_changeset_id": item.SourceChangesetID,
	}
}

func fileDiffPreview(item changeset.FileChange, maxBytes int) (string, bool) {
	diff := changeset.UnifiedDiffForFile(item)
	if diff == "" || maxBytes <= 0 {
		return "", false
	}
	preview := trimDiffPreview(diff, diffPreviewLines)
	if len(preview) <= maxBytes {
		return preview, preview != diff
	}
	suffix := "\n... (file diff preview truncated; see the changeset diff resource)"
	if maxBytes <= len(suffix) {
		return suffix[:maxBytes], true
	}
	limit := maxBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(preview[:limit]) {
		limit--
	}
	return preview[:limit] + suffix, true
}

func trimDiffPreview(diff string, maxLines int) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= maxLines {
		return diff
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n... (%d more lines; see the changeset diff resource)", len(lines)-maxLines)
}

func changeDiffResult(item changeset.Changeset) *mcp.CallToolResult {
	return changeDiffResultFromDTO(item, changeSummaryDTO(item))
}

func changeDiffResultFromDTO(item changeset.Changeset, dto map[string]any) *mcp.CallToolResult {
	diffMeta, _ := dto["diff"].(map[string]any)
	mode, _ := diffMeta["mode"].(string)
	fallback := fmt.Sprintf("Changeset %s digest=%s files=%d diff_mode=%s", item.ID, item.Digest, len(item.Files), mode)
	// Keep the first text content useful even for hosts that do not consume
	// ARC structuredContent. The public wrapper renders the same data again,
	// while direct MCP clients still receive a Markdown diff in the session.
	if markdown := changesetDiffMarkdown(dto); markdown != "" {
		fallback += "\n\n" + markdown
	}
	return mcp.NewToolResultStructured(dto, fallback)
}

func changesetDiffMarkdown(dto map[string]any) string {
	files := changesetResultFiles(dto["files"])
	var builder strings.Builder
	truncated := false
	for _, file := range files {
		diff, _ := file["diff"].(string)
		path, _ := file["path"].(string)
		if strings.TrimSpace(diff) == "" || path == "" {
			continue
		}
		label := path
		if newPath, _ := file["new_path"].(string); newPath != "" && newPath != path {
			label += " → " + newPath
		}
		op, _ := file["operation"].(string)
		fmt.Fprintf(&builder, "#### `%s`", label)
		if op != "" {
			builder.WriteString(" · ")
			builder.WriteString(op)
		}
		builder.WriteString("\n\n```diff\n")
		builder.WriteString(diff)
		builder.WriteString("\n```")
		if value, _ := file["diff_truncated"].(bool); value {
			truncated = true
		}
	}
	if builder.Len() > 0 {
		diffMeta, _ := dto["diff"].(map[string]any)
		if diffMeta["mode"] == "resource" {
			truncated = true
		}
		if truncated {
			if resourceURI, _ := diffMeta["resource_uri"].(string); resourceURI != "" {
				builder.WriteString("\n\n> 完整变更见 Changeset Resource。")
			}
		}
		return builder.String()
	}

	diffMeta, _ := dto["diff"].(map[string]any)
	diff, _ := diffMeta["unified_diff"].(string)
	if diff == "" {
		diff, _ = diffMeta["unified_diff_preview"].(string)
	}
	if diff == "" {
		return ""
	}
	return "```diff\n" + trimDiffPreview(diff, diffPreviewLines) + "\n```"
}

func changesetResultFiles(value any) []map[string]any {
	switch files := value.(type) {
	case []map[string]any:
		return files
	case []any:
		result := make([]map[string]any, 0, len(files))
		for _, raw := range files {
			if file, ok := raw.(map[string]any); ok {
				result = append(result, file)
			}
		}
		return result
	default:
		return nil
	}
}

func (r *Runtime) changeError(envReq envelope.Request, remoteSessionID, workspace string, err error) (*mcp.CallToolResult, error) {
	message := err.Error()
	code := "CHANGESET_ERROR"
	switch {
	case errors.Is(err, changeset.ErrNotFound):
		code = "CHANGESET_NOT_FOUND"
	case strings.Contains(message, "no match"):
		code = "PATCH_CONTEXT_NOT_FOUND"
	case strings.Contains(message, "ambiguous_match"):
		code = "PATCH_CONTEXT_AMBIGUOUS"
	case strings.Contains(message, "hunk") || strings.Contains(message, "overlap"):
		code = "PATCH_HUNKS_OVERLAP"
	case strings.Contains(message, "rollback:") && !strings.Contains(message, "rollback: <nil>"):
		code = "PATCH_ROLLBACK_FAILED"
	case errors.Is(err, changeset.ErrStaleRevision):
		code = "STALE_REVISION"
	case errors.Is(err, changeset.ErrConflict):
		code = "PATCH_CONFLICT"
	case strings.Contains(message, "digest mismatch"):
		code = "PATCH_CONFLICT"
	case strings.Contains(message, "instruction conflict"):
		code = "INSTRUCTION_CONFLICT"
	case strings.Contains(message, "patch has"):
		code = "PATCH_TOO_MANY_FILES"
	case strings.Contains(message, "appears more than once"):
		code = "PATCH_DUPLICATE_PATH"
	case strings.Contains(message, "sha256 required"):
		code = "REVISION_REQUIRED"
	case strings.Contains(message, "new_path required"):
		code = "MISSING_ARGUMENT"
	}
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, workspace, nil, code, message)
	response.RemoteSessionID = remoteSessionID
	if code == "STALE_REVISION" || code == "PATCH_CONFLICT" || code == "PATCH_CONTEXT_NOT_FOUND" || code == "PATCH_CONTEXT_AMBIGUOUS" || code == "PATCH_HUNKS_OVERLAP" {
		path := changesetErrorPath(message)
		if path != "" {
			addRecoveryAction(&response, "file_read", "read the current file revision before generating a new Changeset", map[string]any{
				"remote_session_id": remoteSessionID,
				"path":              path,
			})
		} else {
			addRecoveryAction(&response, "context_query", "list or locate the affected file before retrying", map[string]any{
				"remote_session_id": remoteSessionID,
				"action":            "list",
			})
		}
	}
	if code == "REVISION_REQUIRED" {
		addRecoveryAction(&response, "file_read", "read the current revision of the target file, then retry the operation with its base_sha256", map[string]any{
			"remote_session_id": remoteSessionID,
		})
	}
	if code == "PATCH_DUPLICATE_PATH" {
		addRecoveryAction(&response, "change_manage", "merge operations for the same path into a single operation", map[string]any{
			"remote_session_id": remoteSessionID,
			"action":            "prepare",
		})
	}
	if code == "PATCH_TOO_MANY_FILES" {
		addRecoveryActions(&response,
			nextActionWithReason("change_manage", "split the patch into per-file Changesets", map[string]any{"remote_session_id": remoteSessionID, "action": "prepare"}),
			nextActionWithReason("change_execute", "retry with fewer files per call", map[string]any{"remote_session_id": remoteSessionID}),
		)
	}
	return r.resultJSON(response)
}

func changesetErrorPath(message string) string {
	for _, marker := range []string{"file revision is stale:", "digest mismatch for ", "apply ", "patch context did not match in "} {
		index := strings.LastIndex(strings.ToLower(message), marker)
		if index < 0 {
			continue
		}
		candidate := strings.TrimSpace(message[index+len(marker):])
		if colon := strings.Index(candidate, ":"); colon >= 0 {
			candidate = candidate[:colon]
		}
		candidate = strings.Trim(strings.TrimSpace(candidate), "`\"'")
		if candidate != "" && !strings.ContainsAny(candidate, " \t\r\n") {
			return candidate
		}
	}
	return ""
}
