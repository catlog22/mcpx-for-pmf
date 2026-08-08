package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/edit"
	"mcpx/internal/envelope"
	"mcpx/internal/idempotency"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

const cleanEditIdempotencyOperation = "edit"

type storedEditError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Path         string `json:"path,omitempty"`
	Index        int    `json:"index"`
	Current      string `json:"current_sha256,omitempty"`
	ChangedLines int    `json:"changed_lines,omitempty"`
}

type storedEditResult struct {
	EditID string           `json:"edit_id"`
	Result edit.BatchResult `json:"result"`
	Error  *storedEditError `json:"error,omitempty"`
}

func (s storedEditError) applyError() *edit.ApplyError {
	return &edit.ApplyError{Code: s.Code, Message: s.Message, Path: s.Path, Index: s.Index, Current: s.Current, ChangedLines: s.ChangedLines}
}

// toolEdit is the clean-core unified file edit entry (new engine, not changeset).
func (r *Runtime) toolEdit(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}

	edits, err := parseCleanEdits(envReq.Payload)
	if err != nil {
		return r.editToolError(envReq, session, err)
	}
	effective := r.effectiveConfig(session.WorkspacePath)
	for _, item := range edits {
		for _, path := range []string{item.Path, item.NewPath} {
			if path != "" && security.MatchFile(effective.Security.Files, path) == security.Deny {
				response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName,
					nil, "FILE_DENIED", "file denied by policy")
				response.Error.Details["path"] = path
				response.RemoteSessionID = session.ID
				return r.resultJSON(response)
			}
		}
	}

	idempotencyKey := strings.TrimSpace(stringPayload(envReq.Payload, "idempotency_key"))
	fingerprint := cleanEditFingerprint(envReq, edits)
	var idemKey idempotency.Key
	var claim idempotency.Claim
	claimed := idempotencyKey != "" && r.idempotency != nil
	if claimed {
		idemKey = idempotency.Key{RemoteSessionID: session.ID, PrincipalID: principal.ID, Operation: cleanEditIdempotencyOperation, Value: idempotencyKey}
		var claimErr error
		claim, claimErr = r.idempotency.Claim(ctx, idemKey, fingerprint, cleanEditRecordTTL)
		if claimErr != nil {
			return r.editToolError(envReq, session, claimErr)
		}
		switch claim.Kind {
		case idempotency.ClaimConflict:
			return r.editIdempotencyConflict(envReq, session, fingerprint, claim.Record.Fingerprint)
		case idempotency.ClaimReplay:
			return r.replayStoredEdit(envReq, session, claim.Record.Response)
		case idempotency.ClaimInDoubt:
			if reconciled, ok := r.reconcilePendingEdit(ctx, envReq, session, principal.ID, claim, idemKey, fingerprint); ok {
				return reconciled, nil
			}
			return r.editIdempotencyInDoubt(envReq, session, claim.Record)
		case idempotency.ClaimWait:
			record, waitErr := r.idempotency.Wait(ctx, claim, idemKey)
			if waitErr != nil {
				return r.editIdempotencyPending(envReq, session, waitErr.Error())
			}
			return r.replayStoredEdit(envReq, session, record.Response)
		case idempotency.ClaimPending:
			return r.editIdempotencyPending(envReq, session, "the same idempotency request is still running")
		}
		if claim.Kind == idempotency.ClaimOwner {
			if reconciled, ok := r.reconcilePendingEdit(ctx, envReq, session, principal.ID, claim, idemKey, fingerprint); ok {
				return reconciled, nil
			}
		}
	}

	editID := newRuntimeID("edit", 12)
	preparedPersisted := false
	var preparedResult edit.BatchResult
	result, err := edit.ApplyBatchWithHook(edit.BatchRequest{
		WorkspaceRoot: session.WorkspacePath,
		Edits:         edits,
	}, func(prepared edit.BatchResult) error {
		preparedResult = prepared
		stored := storedEditResult{EditID: editID, Result: prepared}
		if err := r.saveCleanEditRecord(ctx, session.ID, principal.ID, editID, "pending", prepared); err != nil {
			return err
		}
		if claimed {
			encoded, encodeErr := json.Marshal(stored)
			if encodeErr != nil {
				return encodeErr
			}
			metadata, _ := json.Marshal(map[string]any{"edit_id": editID, "paths": editPaths(prepared)})
			if err := r.idempotency.UpdatePending(ctx, idemKey, fingerprint, encoded, metadata); err != nil {
				return err
			}
		}
		preparedPersisted = true
		return nil
	})
	if err != nil {
		if preparedPersisted {
			_ = r.saveCleanEditRecord(ctx, session.ID, principal.ID, editID, "in_doubt", preparedResult)
			if claimed {
				_ = r.idempotency.MarkInDoubt(ctx, idemKey, fingerprint, []byte(`{"recovery":"read current file SHA values before retrying"}`))
			}
			return r.editIdempotencyInDoubt(envReq, session, idempotency.Record{Key: idemKey, Fingerprint: fingerprint, State: idempotency.StateInDoubt})
		}
		if claimed {
			stored := storedEditResult{Error: storedApplyError(err)}
			encoded, _ := json.Marshal(stored)
			_ = r.idempotency.Complete(ctx, idemKey, fingerprint, idempotency.StateFailed, encoded, nil)
		}
		return r.editToolError(envReq, session, err)
	}
	if err := r.saveCleanEditRecord(ctx, session.ID, principal.ID, editID, "succeeded", result); err != nil {
		if claimed {
			_ = r.idempotency.MarkInDoubt(ctx, idemKey, fingerprint, []byte(`{"recovery":"edit record persistence failed; inspect file SHA values"}`))
		}
		return r.editIdempotencyInDoubt(envReq, session, idempotency.Record{Key: idemKey, Fingerprint: fingerprint, State: idempotency.StateInDoubt})
	}
	if claimed {
		stored := storedEditResult{EditID: editID, Result: result}
		encoded, _ := json.Marshal(stored)
		metadata, _ := json.Marshal(map[string]any{"edit_id": editID, "paths": editPaths(result)})
		if err := r.idempotency.Complete(ctx, idemKey, fingerprint, idempotency.StateSucceeded, encoded, metadata); err != nil {
			return r.editIdempotencyInDoubt(envReq, session, idempotency.Record{Key: idemKey, Fingerprint: fingerprint, State: idempotency.StateInDoubt})
		}
	}

	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{
		RemoteSessionID: session.ID,
		Type:            "edit.applied",
		Summary:         fmt.Sprintf("edit %d files, %d changed lines", len(result.Results), result.TotalChangedLines),
	})
	r.observeCleanEdit(ctx, envReq, session, editID, result)
	r.logAudit(audit.Event{
		RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName,
		Tool: "edit", Status: "ok",
		Detail: map[string]any{"files": len(result.Results), "total_changed_lines": result.TotalChangedLines},
	})
	return r.editToolSuccess(envReq, session, editID, result, false)
}

func (r *Runtime) editToolSuccess(envReq envelope.Request, session remotesession.Session, editID string, result edit.BatchResult, replay bool) (*mcp.CallToolResult, error) {
	data := editResponseData(session.ID, editID, result, replay)
	data["remote_session_id"] = session.ID
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
}

func (r *Runtime) editToolError(envReq envelope.Request, session remotesession.Session, err error) (*mcp.CallToolResult, error) {
	var ae *edit.ApplyError
	if errors.As(err, &ae) {
		details := map[string]any{}
		if ae.Path != "" {
			details["path"] = ae.Path
		}
		if ae.Index >= 0 {
			details["replacement_index"] = ae.Index
		}
		if ae.Current != "" {
			details["current_sha256"] = ae.Current
		}
		if ae.ChangedLines > 0 {
			details["total_changed_lines"] = ae.ChangedLines
		}
		code := ae.Code
		if code == "" {
			code = "EDIT_FAILED"
		}
		msg := ae.Error()
		recovery := editRecovery(code, ae)
		details["recovery"] = recovery
		suggestedNext := editSuggestedNext(code, session.ID, ae)
		details["suggested_next"] = suggestedNext
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, code, msg)
		for key, value := range details {
			response.Error.Details[key] = value
		}
		response.Error.Recovery = &envelope.Recovery{
			Action: suggestedNext["tool"].(string),
			Tool:   suggestedNext["tool"].(string),
			Arguments: map[string]any{
				"remote_session_id": session.ID,
				"path":              ae.Path,
			},
		}
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, "EDIT_FAILED", err.Error())
	response.RemoteSessionID = session.ID
	return r.resultJSON(response)
}

func editRecovery(code string, ae *edit.ApplyError) string {
	switch code {
	case "STALE_REVISION":
		return "re-read the file to get the latest sha256, then retry edit with the same idempotency_key and updated base_sha256"
	case "MATCH_NOT_FOUND", "MATCH_AMBIGUOUS":
		return "re-read the file and use a longer unique match snippet for the failed replacement"
	case "TOO_MANY_CHANGES":
		return fmt.Sprintf("split the edit into smaller batches; max total changed lines is %d", edit.MaxChangedLines)
	case "FILE_DENIED", "POLICY_DENIED":
		return "adjust the path or obtain policy approval"
	default:
		if ae != nil && ae.Message != "" {
			return ae.Message
		}
		return "inspect error details and retry with corrected parameters"
	}
}

func editSuggestedNext(code, remoteSessionID string, ae *edit.ApplyError) map[string]any {
	next := map[string]any{
		"remote_session_id": remoteSessionID,
	}
	switch code {
	case "STALE_REVISION":
		next["tool"] = "read"
		if ae != nil && ae.Path != "" {
			next["path"] = ae.Path
		}
		if ae != nil && ae.Current != "" {
			next["note"] = "use current_sha256 as base_sha256 on retry"
		}
	case "TOO_MANY_CHANGES":
		next["tool"] = "edit"
		next["note"] = "split edits so total +/- lines <= 1000"
	default:
		next["tool"] = "edit"
	}
	return next
}

func (r *Runtime) observeCleanEdit(ctx context.Context, envReq envelope.Request, session remotesession.Session, editID string, result edit.BatchResult) {
	if r == nil || r.observation == nil {
		return
	}
	paths := make([]string, 0, len(result.Results))
	for _, item := range result.Results {
		paths = append(paths, item.Path)
	}
	payload, _ := json.Marshal(map[string]any{
		"edit_id":             editID,
		"diff_summary":        boundedDiffPreview(result.DiffSummary, cleanDiffTotalPreviewMaxBytes).Text,
		"diff_bytes":          len(result.DiffSummary),
		"diff_truncated":      len(result.DiffSummary) > cleanDiffTotalPreviewMaxBytes,
		"total_changed_lines": result.TotalChangedLines,
		"paths":               paths,
		"results":             editResponseData(session.ID, editID, result, false)["results"],
	})
	summary := fmt.Sprintf("edit %d file(s), %d changed lines", len(result.Results), result.TotalChangedLines)
	_ = r.observation.Record(ctx, observation.Event{
		Workspace:       session.WorkspaceName,
		RemoteSessionID: session.ID,
		RequestID:       envReq.RequestID,
		Tool:            "edit",
		Type:            observation.TypeFileChanged,
		Status:          "succeeded",
		Purpose:         envReq.Intent,
		Intent:          envReq.Intent,
		Output:          payload,
		Summary:         summary,
		Path:            strings.Join(paths, ","),
	})
}

func parseCleanEdits(payload map[string]any) ([]edit.FileEdit, error) {
	if payload == nil {
		return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "edits required", Index: -1, Err: edit.ErrInvalidInput}
	}
	raw, ok := payload["edits"]
	if !ok || raw == nil {
		// Single-file shorthand: top-level path + replacements/content.
		if path, _ := payload["path"].(string); strings.TrimSpace(path) != "" {
			raw = []any{payload}
		} else {
			return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "edits required", Index: -1, Err: edit.ErrInvalidInput}
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var edits []edit.FileEdit
	if err := json.Unmarshal(encoded, &edits); err != nil {
		return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "invalid edits payload", Index: -1, Err: edit.ErrInvalidInput}
	}
	if len(edits) == 0 {
		return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "edits required", Index: -1, Err: edit.ErrInvalidInput}
	}
	for i := range edits {
		if strings.TrimSpace(edits[i].Operation) == "" {
			return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: fmt.Sprintf("edits[%d].operation required", i), Index: i, Err: edit.ErrInvalidInput}
		}
	}
	return edits, nil
}
