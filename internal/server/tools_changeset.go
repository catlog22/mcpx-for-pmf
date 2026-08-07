package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/changeset"
	"mcpx/internal/envelope"
	"mcpx/internal/file"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

func (r *Runtime) toolChangePrepare(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	clientRequestID, _ := envReq.Payload["idempotency_key"].(string)
	prepared, replayed, err := r.changesets.PrepareIdempotentWithOptions(ctx, session.ID, principal.ID, strings.TrimSpace(clientRequestID), session.WorkspacePath, summary, operations, changeset.PrepareOptions{})
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	if replayed {
		payload := changeSummaryDTO(prepared)
		payload["idempotent_replay"] = true
		return changeDiffResultFromDTO(prepared, payload), nil
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.prepared", OperationID: prepared.ID, Summary: prepared.Summary})
	return changeDiffResult(prepared), nil
}

func (r *Runtime) toolChangeDiff(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func (r *Runtime) toolChangeHistory(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	limit := intPayload(envReq.Payload, "limit")
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	history, err := r.changesets.History(ctx, session.ID, limit)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	digest := changeHistoryDigest(history)
	knownDigest := strings.TrimSpace(stringPayload(envReq.Payload, "known_history_digest"))
	notModified := knownDigest != "" && knownDigest == digest
	data := map[string]any{
		"history_digest": digest,
		"history_limit":  limit,
		"not_modified":   notModified,
		"summary":        changeHistorySummary(session.ID, history),
	}
	if notModified {
		data["changesets"] = []changeset.Changeset{}
		data["message"] = "Changeset history unchanged; reuse the previously returned history."
	} else {
		data["changesets"] = history
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
}

type changeHistoryDigestItem struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	Summary           string `json:"summary"`
	Digest            string `json:"digest"`
	SourceChangesetID string `json:"source_changeset_id,omitempty"`
	CreatedAt         int64  `json:"created_at"`
	AppliedAt         int64  `json:"applied_at,omitempty"`
	DiscardedAt       int64  `json:"discarded_at,omitempty"`
}

func changeHistoryDigest(history []changeset.Changeset) string {
	items := make([]changeHistoryDigestItem, 0, len(history))
	for _, item := range history {
		digestItem := changeHistoryDigestItem{
			ID: item.ID, Status: item.Status, Summary: item.Summary, Digest: item.Digest,
			SourceChangesetID: item.SourceChangesetID, CreatedAt: item.CreatedAt.UnixMilli(),
		}
		if item.AppliedAt != nil {
			digestItem.AppliedAt = item.AppliedAt.UnixMilli()
		}
		if item.DiscardedAt != nil {
			digestItem.DiscardedAt = item.DiscardedAt.UnixMilli()
		}
		items = append(items, digestItem)
	}
	encoded, _ := json.Marshal(items)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func changeHistorySummary(remoteSessionID string, history []changeset.Changeset) map[string]any {
	drafts := make([]map[string]any, 0)
	for _, item := range history {
		if item.Status != "draft" {
			continue
		}
		paths := make([]string, 0, len(item.Files))
		for _, file := range item.Files {
			path := file.Path
			if file.NewPath != "" && file.NewPath != file.Path {
				path += " -> " + file.NewPath
			}
			paths = append(paths, path)
		}
		drafts = append(drafts, map[string]any{
			"changeset_id": item.ID,
			"digest":       item.Digest,
			"summary":      item.Summary,
			"files":        paths,
			"next_actions": []map[string]any{
				nextActionWithReason("change", "继续应用该草稿时复制 changeset_id 和 digest", map[string]any{
					"action":            "apply",
					"remote_session_id": remoteSessionID,
					"changeset_id":      item.ID,
					"expected_digest":   item.Digest,
				}),
				nextActionWithReason("change", "该草稿不再需要时一次丢弃，避免继续占用交付状态", map[string]any{
					"action":            "discard",
					"remote_session_id": remoteSessionID,
					"changeset_id":      item.ID,
				}),
			},
		})
	}
	return map[string]any{
		"total":         len(history),
		"active_drafts": drafts,
		"draft_count":   len(drafts),
	}
}

func (r *Runtime) toolChangeDiscard(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	discarded, err := r.changesets.Discard(ctx, item.ID)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.discarded", OperationID: item.ID, Summary: item.Summary})
	return changeDiffResult(discarded), nil
}

func (r *Runtime) toolChangeRevert(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func (r *Runtime) changeRequest(ctx context.Context, req *mcp.CallToolRequest, edit bool) (envelope.Request, auth.Principal, remotesession.Session, *mcp.CallToolResult) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return envReq, principal, remotesession.Session{}, fail
	}
	if edit {
		if err := validatePurpose(envReq.Intent); err != nil {
			response := envelope.Fail(envelope.StatusError, envReq.RequestID, envReq.Workspace, nil, "PURPOSE_REQUIRED", err.Error())
			result, _ := r.resultJSON(response)
			return envReq, principal, remotesession.Session{}, result
		}
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
	applyResult, err := r.changesets.Apply(ctx, item.ID, session.WorkspacePath)
	if err != nil {
		return r.changeError(envReq, session.ID, session.WorkspaceName, err)
	}
	appliedItem := item
	if current, getErr := r.changesets.Get(ctx, item.ID); getErr == nil {
		appliedItem = current
	}
	r.observeAppliedChangeset(ctx, envReq, session, appliedItem)
	principal, err := r.principalFromContext(ctx)
	if err == nil && principal.ID == principalID {
		_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "changeset.applied", OperationID: item.ID, Summary: item.Summary})
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "change_execute", Status: "ok", Detail: map[string]any{"changeset_id": item.ID, "digest": item.Digest}})
	payload := changeSummaryDTO(appliedItem)
	payload["applied"] = true
	payload["apply"] = applyResult
	if raw, ok := envReq.Payload["verify"].([]any); ok {
		verify := make([]string, 0, len(raw))
		for _, item := range raw {
			if step, ok := item.(string); ok && strings.TrimSpace(step) != "" {
				verify = append(verify, strings.TrimSpace(step))
			}
		}
		if len(verify) > 0 {
			payload["verify"] = r.runVerifySteps(ctx, envReq, session, verify)
		}
	}
	return compactToolResult(payload, fmt.Sprintf("Changeset %s applied.", item.ID)), nil
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
		if !changeset.IsDirectoryChange(f) {
			switch f.Operation {
			case "update", "rename":
				file["original_format"] = formatDTO(f.OriginalFormat)
				file["proposed_format"] = formatDTO(f.ProposedFormat)
			case "create":
				file["proposed_format"] = formatDTO(f.ProposedFormat)
			case "delete":
				file["original_format"] = formatDTO(f.OriginalFormat)
			}
		}
		file["format_preserved"] = f.FormatPreserved
		if changeset.IsDirectoryChange(f) {
			file["is_directory"] = true
			file["rollback"] = "retained_backup"
			file["delete_mode"] = "directory_recursive"
			file["deleted_files"] = f.DeletedFiles
			file["deleted_directories"] = f.DeletedDirs
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
	dto := map[string]any{
		"changeset_id": item.ID, "remote_session_id": item.RemoteSessionID,
		"status": item.Status, "summary": item.Summary, "digest": item.Digest,
		"files": files, "diff": diff, "created_at": item.CreatedAt,
		"source_changeset_id": item.SourceChangesetID,
	}
	if item.DiscardedAt != nil {
		dto["discarded_at"] = item.DiscardedAt
	}
	if strings.TrimSpace(item.Digest) != "" {
		dto["expected_digest"] = item.Digest
		if item.Status == "draft" {
			nextTool := "change"
			nextArguments := map[string]any{
				"action":            "apply",
				"remote_session_id": item.RemoteSessionID,
				"changeset_id":      item.ID,
				"expected_digest":   item.Digest,
			}
			nextReason := "应用此 Changeset 时必须原样复制 digest 到 expected_digest；不要使用 diff 统计、快照 ID 或空值。"
			if item.SourceChangesetID != "" {
				nextTool = "change"
				nextReason = "回滚操作必须继续使用源 Changeset ID；digest 只用于识别当前变更，不要把生成的回滚草稿 ID 当作源 ID。"
				nextArguments = map[string]any{
					"action":            "revert",
					"remote_session_id": item.RemoteSessionID,
					"changeset_id":      item.SourceChangesetID,
				}
			}
			dto["next_action"] = nextActionWithReason(nextTool, nextReason, nextArguments)
		}
	}
	if deleteSummary := deleteSummaryDTO(item.Files); deleteSummary != nil {
		dto["delete_summary"] = deleteSummary
	}
	return dto
}

func formatDTO(format file.Format) map[string]any {
	result := map[string]any{
		"charset":            format.Charset,
		"bom":                format.BOM,
		"line_ending":        format.LineEnding,
		"line_ending_counts": map[string]int{"lf": format.LineEndingCounts.LF, "crlf": format.LineEndingCounts.CRLF, "cr": format.LineEndingCounts.CR},
	}
	if format.FinalNewline == nil {
		result["final_newline"] = nil
	} else {
		result["final_newline"] = *format.FinalNewline
	}
	return result
}

func deleteSummaryDTO(files []changeset.FileChange) map[string]any {
	topLevelDirectories, topLevelFiles := 0, 0
	deletedFiles, deletedDirectories := 0, 0
	for _, item := range files {
		if item.Operation != "delete" {
			continue
		}
		if changeset.IsDirectoryChange(item) {
			topLevelDirectories++
			deletedFiles += item.DeletedFiles
			deletedDirectories += item.DeletedDirs
			if item.DeletedDirs == 0 {
				// Changesets created before deletion statistics were persisted
				// still identify the selected directory safely.
				deletedDirectories++
			}
			continue
		}
		topLevelFiles++
		if item.DeletedFiles > 0 {
			deletedFiles += item.DeletedFiles
		} else {
			deletedFiles++
		}
	}
	if topLevelDirectories == 0 && topLevelFiles == 0 {
		return nil
	}
	mode := "file"
	if topLevelDirectories > 0 && topLevelFiles > 0 {
		mode = "mixed"
	} else if topLevelDirectories > 0 {
		mode = "directory_recursive"
	}
	display := "逐项删除 " + strconv.Itoa(topLevelFiles) + " 个顶层文件。"
	if topLevelDirectories > 0 {
		display = fmt.Sprintf("目录级递归删除 %d 个顶层目录", topLevelDirectories)
		if topLevelFiles > 0 {
			display += fmt.Sprintf("，并逐项删除 %d 个顶层文件", topLevelFiles)
		}
		display += fmt.Sprintf("；共删除 %d 个文件、%d 个目录。", deletedFiles, deletedDirectories)
	}
	return map[string]any{
		"mode":                  mode,
		"top_level_directories": topLevelDirectories,
		"top_level_files":       topLevelFiles,
		"deleted_files":         deletedFiles,
		"deleted_directories":   deletedDirectories,
		"display":               display,
	}
}

func deleteSummaryDisplay(dto map[string]any) string {
	summary, _ := dto["delete_summary"].(map[string]any)
	display, _ := summary["display"].(string)
	return strings.TrimSpace(display)
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
	if applied, _ := dto["applied"].(bool); applied {
		delete(dto, "next_action")
	}
	diffMeta, _ := dto["diff"].(map[string]any)
	mode, _ := diffMeta["mode"].(string)
	fallback := fmt.Sprintf("Changeset %s digest=%s files=%d diff_mode=%s", item.ID, item.Digest, len(item.Files), mode)
	if expectedDigest, _ := dto["expected_digest"].(string); expectedDigest != "" {
		fallback += fmt.Sprintf("\n\nexpected_digest 必须原样复制为：%s（不要使用 diff 统计、快照 ID 或空值）", expectedDigest)
	}
	if summary := deleteSummaryDisplay(dto); summary != "" {
		fallback += " · " + summary
	}
	// Keep the first text content useful for hosts and models. The public
	// wrapper preserves the full result in response metadata while rendering
	// this Markdown diff directly in the session.
	if markdown := changesetDiffMarkdown(dto); markdown != "" {
		fallback += "\n\n" + markdown
	}
	return compactToolResult(dto, fallback)
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
	case (strings.Contains(message, "base_sha256") || strings.Contains(message, "expected_sha256")) && strings.Contains(message, "required"):
		// Before STALE: missing base must not be classified as revision mismatch.
		code = "REVISION_REQUIRED"
	case strings.Contains(message, "no match"):
		code = "PATCH_CONTEXT_NOT_FOUND"
	case strings.Contains(message, "ambiguous_match"):
		code = "PATCH_CONTEXT_AMBIGUOUS"
	case strings.Contains(message, "hunk") || strings.Contains(message, "overlap") || strings.Contains(message, "expected Unified Diff"):
		// Check before generic "does not match" (hunk line counts also use that phrase).
		code = "PATCH_HUNKS_OVERLAP"
	case strings.Contains(message, "does not match"):
		// Correct base + wrong patch context/removed line — not a stale file revision.
		// Covers "context does not match" and "removed line does not match".
		code = "PATCH_CONTEXT_MISMATCH"
	case strings.Contains(message, "patch apply failed"):
		// Generic patch apply errors (syntax, parse) that are not hunk/context-specific.
		code = "PATCH_APPLY_FAILED"
	case strings.Contains(message, "rollback:") && !strings.Contains(message, "rollback: <nil>"):
		code = "PATCH_ROLLBACK_FAILED"
	case errors.Is(err, changeset.ErrStaleRevision):
		code = "STALE_REVISION"
	case errors.Is(err, changeset.ErrFormatChanged):
		code = "FORMAT_CHANGED"
	case errors.Is(err, changeset.ErrDiscarded):
		code = "CHANGESET_DISCARDED"
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
	case strings.Contains(message, "delete/create conflict"):
		code = "DELETE_CREATE_CONFLICT"
	case strings.Contains(message, "new_path required"):
		code = "MISSING_ARGUMENT"
	}
	patchGuidance := "先用 source_read(view=full) 获取当前内容、sha256 和 format（含 line_ending）；重新生成补丁时必须保留目标文件原有换行格式，并保留字符集、BOM、换行和末尾换行状态，不要让格式差异把局部修改扩大为整文件格式变更。为保持最小 diff，使用带当前 base_sha256 的 replace_exact/insert_before/insert_after，并按当前 format 组织 match 和 replacement；只有生成并核对局部 hunk 后才使用 update.patch。"
	patchFormatGuidance := ""
	if code == "PATCH_HUNKS_OVERLAP" && strings.Contains(message, "expected Unified Diff hunk header") {
		patchFormatGuidance = "patch 必须是标准 unified diff：以 --- a/<path> 和 @@ -起始行,行数 +起始行,行数 @@ 开头；不接受 apply_patch 的 *** Begin Patch / *** Update File 格式。若难以生成局部 hunk，改用 replace_exact/insert_before/insert_after。"
		message += "；" + patchFormatGuidance
	}
	if code == "PATCH_HUNKS_OVERLAP" && strings.Contains(message, "hunk line counts do not match its header") {
		patchFormatGuidance = "patch hunk 头的行数（@@ -起始,旧行数 +起始,新行数 @@）必须与实际 -/+ 行数一致；请逐行核对 hunk 或改用 replace_exact/insert_before/insert_after。"
		message += "；" + patchFormatGuidance
	}
	if code == "STALE_REVISION" {
		message += "；base_sha256 与当前文件版本不一致。调用 source_read(view=file, mode=full, path=…) 后，将返回的 sha256 原样写入失败 op 的 base_sha256 再重试该 path。"
	}
	if code == "REVISION_REQUIRED" {
		message += "；缺少 base_sha256。先 source_read(view=file, mode=full) 取返回字段 sha256，原样填入 operations[].base_sha256（字段名固定，不要用 revision/hash/digest）。"
	}
	if code == "PATCH_CONTEXT_MISMATCH" || code == "PATCH_APPLY_FAILED" {
		message += "；补丁或匹配上下文与文件内容不一致（base_sha256 可能仍正确）。按 source_read 返回的 content 重建 match/replacement，或改用 replace_exact；不要重试同一 patch。"
	}
	if code == "FORMAT_CHANGED" {
		message += "；普通变更必须保留目标文件的字符集、BOM、换行格式和末尾换行状态；先读取当前文件的 format，再按该格式生成内容。只有明确要求格式化时才使用 change_execute(format=true)。"
	}
	if code == "CHANGESET_DISCARDED" {
		message += "；该草稿已明确丢弃，不能继续应用；请重新准备新的 Changeset。"
	}
	if code == "PATCH_HUNKS_OVERLAP" || code == "PATCH_CONTEXT_MISMATCH" || code == "PATCH_APPLY_FAILED" || code == "PATCH_CONTEXT_NOT_FOUND" || code == "PATCH_CONTEXT_AMBIGUOUS" {
		message += "；" + patchGuidance
	}
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, workspace, nil, code, message)
	response.RemoteSessionID = remoteSessionID
	path := changesetErrorPath(message)
	ordinal := changesetErrorOrdinal(message)
	attachChangeFieldSemantics(&response, code, path, ordinal, remoteSessionID)
	if code == "STALE_REVISION" || code == "PATCH_CONFLICT" || code == "PATCH_CONTEXT_NOT_FOUND" || code == "PATCH_CONTEXT_AMBIGUOUS" || code == "PATCH_HUNKS_OVERLAP" || code == "PATCH_CONTEXT_MISMATCH" || code == "PATCH_APPLY_FAILED" {
		if path != "" {
			addRecoveryAction(&response, "file_read", "调用 source_read 读取当前文件；将返回的 sha256 原样写入 base_sha256 后仅重试失败 op", map[string]any{
				"remote_session_id": remoteSessionID,
				"path":              path,
				"view":              "file",
				"mode":              "full",
			})
		} else {
			addRecoveryAction(&response, "context_query", "list or locate the affected file before retrying", map[string]any{
				"remote_session_id": remoteSessionID,
				"action":            "list",
			})
		}
	}
	if code == "REVISION_REQUIRED" {
		arguments := map[string]any{"remote_session_id": remoteSessionID, "view": "file", "mode": "full"}
		if path != "" {
			arguments["path"] = path
		}
		addRecoveryAction(&response, "file_read", "source_read 后复制字段 sha256 → operations[].base_sha256，再 change", arguments)
	}
	if code == "FORMAT_CHANGED" {
		arguments := map[string]any{"remote_session_id": remoteSessionID, "view": "file", "mode": "full"}
		if path != "" {
			arguments["path"] = path
		}
		addRecoveryAction(&response, "file_read", "读取 format 与 sha256 后按原 line_ending 重写 match/content，并带上 base_sha256", arguments)
	}
	if code == "PATCH_DUPLICATE_PATH" {
		addRecoveryAction(&response, "change_manage", "merge operations for the same path into a single operation", map[string]any{
			"remote_session_id": remoteSessionID,
			"action":            "prepare",
		})
	}
	if code == "PATCH_HUNKS_OVERLAP" || code == "PATCH_CONTEXT_MISMATCH" || code == "PATCH_APPLY_FAILED" || code == "FORMAT_CHANGED" {
		if response.Error != nil {
			if response.Error.Details == nil {
				response.Error.Details = map[string]any{}
			}
			response.Error.Details["patch_guidance"] = patchGuidance
			if patchFormatGuidance != "" {
				response.Error.Details["patch_format_guidance"] = patchFormatGuidance
			}
		}
	}
	if code == "DELETE_CREATE_CONFLICT" {
		addRecoveryActions(&response,
			nextActionWithReason("change_execute", "apply the delete operation alone, wait for success, then submit a new create operation", map[string]any{"remote_session_id": remoteSessionID}),
			nextActionWithReason("context_query", "re-enumerate the workspace after deletion before creating files", map[string]any{"remote_session_id": remoteSessionID, "action": "list"}),
		)
	}
	if code == "PATCH_TOO_MANY_FILES" {
		addRecoveryActions(&response,
			nextActionWithReason("change_manage", "split the patch into per-file Changesets", map[string]any{"remote_session_id": remoteSessionID, "action": "prepare"}),
			nextActionWithReason("change_execute", "retry with fewer files per call", map[string]any{"remote_session_id": remoteSessionID}),
		)
	}
	return r.resultJSON(response)
}

// attachChangeFieldSemantics fills machine-readable field maps so models copy
// values instead of inventing field names or aliases.
func attachChangeFieldSemantics(response *envelope.Response, code, path string, ordinal *int, remoteSessionID string) {
	if response == nil || response.Error == nil {
		return
	}
	if response.Error.Details == nil {
		response.Error.Details = map[string]any{}
	}
	d := response.Error.Details
	if path != "" {
		d["path"] = path
	}
	if ordinal != nil {
		d["failed_ordinal"] = *ordinal
	}
	// Fixed copy contract: source_read.sha256 -> change.operations[].base_sha256
	fieldMap := map[string]any{
		"base_sha256": map[string]any{
			"from_tool":  "source_read",
			"from_field": "sha256",
			"to_tool":    "change",
			"to_field":   "operations[].base_sha256",
			"rule":       "copy_verbatim",
			"note":       "形如 sha256:<64 hex>；禁止截断、改前缀或改字段名。create 可省略；delete 可省略；其余改已有文件必填。",
		},
		"match": map[string]any{
			"from_tool":  "source_read",
			"from_field": "content",
			"to_field":   "operations[].match",
			"rule":       "substring_of_content",
			"note":       "必须是 content 中的连续原文，含空白与换行。",
		},
		"replacement": map[string]any{
			"to_field": "operations[].replacement",
			"for":      []string{"replace_exact"},
			"note":     "replace_exact 用 replacement；insert_* 用 content；不要对调。",
		},
		"content": map[string]any{
			"to_field": "operations[].content",
			"for":      []string{"create", "insert_before", "insert_after", "replace_range"},
			"note":     "create/insert_*/replace_range 用 content。",
		},
		"expected_digest": map[string]any{
			"from_tool":  "change",
			"from_field": "digest",
			"to_tool":    "change",
			"to_field":   "expected_digest",
			"rule":       "copy_verbatim",
			"note":       "也可复制 expected_digest 同名字段；禁止填 diff 统计或 changeset_id。",
		},
	}
	d["field_map"] = fieldMap

	switch code {
	case "REVISION_REQUIRED":
		d["required_fields"] = []string{"operations[].base_sha256"}
		d["retry"] = map[string]any{
			"steps": []map[string]any{
				{"tool": "source_read", "arguments": map[string]any{"session_id": remoteSessionID, "view": "file", "mode": "full", "path": path}},
				{"tool": "change", "set": map[string]string{"operations[failed_ordinal].base_sha256": "source_read.sha256"}},
			},
		}
	case "STALE_REVISION":
		d["required_fields"] = []string{"operations[].base_sha256"}
		d["retry"] = map[string]any{
			"steps": []map[string]any{
				{"tool": "source_read", "arguments": map[string]any{"session_id": remoteSessionID, "view": "file", "mode": "full", "path": path}},
				{"tool": "change", "set": map[string]string{"operations[failed_ordinal].base_sha256": "source_read.sha256"}, "note": "仅重试该 path/ordinal；其它文件的 base 可不变"},
			},
		}
	case "PATCH_CONTEXT_NOT_FOUND", "PATCH_CONTEXT_AMBIGUOUS", "PATCH_CONTEXT_MISMATCH", "PATCH_HUNKS_OVERLAP", "PATCH_APPLY_FAILED":
		d["preferred_operations"] = []string{"replace_exact", "insert_before", "insert_after", "delete_exact"}
		d["avoid"] = []string{"update.patch until context is verified"}
		d["retry"] = map[string]any{
			"steps": []map[string]any{
				{"tool": "source_read", "arguments": map[string]any{"session_id": remoteSessionID, "view": "file", "mode": "full", "path": path}},
				{"tool": "change", "note": "用 content 重建 match/replacement；base_sha256=返回的 sha256；优先 exact 操作"},
			},
		}
	case "DELETE_CREATE_CONFLICT":
		d["retry"] = map[string]any{
			"steps": []any{
				"change+apply 仅 delete",
				"source_read/list 确认删除结果",
				"change+apply 仅 create",
			},
		}
	}
}

func changesetErrorOrdinal(message string) *int {
	const prefix = "operation "
	lower := strings.ToLower(message)
	index := strings.Index(lower, prefix)
	if index < 0 {
		return nil
	}
	rest := message[index+len(prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return nil
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return nil
	}
	return &n
}

func changesetErrorPath(message string) string {
	for _, marker := range []string{"file revision is stale:", "patch apply failed:", "file format changed:", "digest mismatch for ", "apply ", "patch context did not match in ", "base_sha256 (expected_sha256 alias) required for ", "expected_sha256 required for ", "base_sha256 required for "} {
		index := strings.LastIndex(strings.ToLower(message), marker)
		if index < 0 {
			continue
		}
		candidate := strings.TrimSpace(message[index+len(marker):])
		// "… required for update: path" or "… required for replace_exact" (no path).
		for _, operation := range []string{"replace_exact", "insert_before", "insert_after", "delete_exact", "replace_range", "create", "update", "rename", "delete"} {
			prefix := operation + ": "
			if strings.HasPrefix(candidate, prefix) {
				candidate = strings.TrimSpace(strings.TrimPrefix(candidate, prefix))
				break
			}
			if candidate == operation || strings.HasPrefix(candidate, operation+"；") || strings.HasPrefix(candidate, operation+";") || strings.HasPrefix(candidate, operation+",") {
				return ""
			}
		}
		// Path is the first token; stop at punctuation or whitespace (incl. fullwidth).
		if cut := strings.IndexAny(candidate, ":；;，,\n\r\t "); cut >= 0 {
			candidate = candidate[:cut]
		}
		candidate = strings.Trim(strings.TrimSpace(candidate), "`\"'")
		if candidate != "" && !strings.ContainsAny(candidate, " \t\r\n") {
			return candidate
		}
	}
	return ""
}
