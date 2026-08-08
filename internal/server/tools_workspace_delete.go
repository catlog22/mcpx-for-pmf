package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/deletion"
	"mcpx/internal/envelope"
	"mcpx/internal/file"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

const deleteRequestMaxTargets = MaxRemoveTargets

var workspaceDeleteSafetyMeta = mcp.Meta{
	"mcpx/safety": map[string]any{
		"classification":                         "constrained_destructive_workspace_file_operation",
		"approval":                               "web_model_user_confirmation_required",
		"filesystem_only":                        true,
		"registered_workspace":                   true,
		"regular_files_and_explicit_directories": true,
		"revision_guarded":                       true,
		"no_shell_execution":                     true,
		"no_implicit_recursive_delete":           true,
		"no_symlink_following":                   true,
		"durable_audit":                          true,
		"bounded_server_chunks":                  true,
		"confirmation_credential":                "server_generated_confirmation_uuid",
	},
}

var workspaceDeletePrepareAnnotation = toolAnnotation{
	ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false,
	Title: "冻结 Workspace 文件/目录删除清单（不执行删除）", Meta: workspaceDeleteSafetyMeta,
}

var workspaceDeleteCommitAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false,
	Title: "提交用户已确认的 Workspace 文件/目录删除", Meta: workspaceDeleteSafetyMeta,
}

func (r *Runtime) toolWorkspaceDeletePrepare(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	if workspace := strings.TrimSpace(stringPayload(envReq.Payload, "workspace")); workspace != "" && workspace != session.WorkspaceName {
		return r.deleteError(envReq, session, "WORKSPACE_MISMATCH", "workspace does not match the registered remote session", map[string]any{"requested_workspace": workspace, "session_workspace": session.WorkspaceName})
	}
	key := strings.TrimSpace(stringPayload(envReq.Payload, "idempotency_key"))
	if key == "" {
		return r.deleteError(envReq, session, "INVALID_REQUEST", "idempotency_key is required", map[string]any{"field": "idempotency_key"})
	}
	targets, err := parseDeleteTargets(envReq.Payload)
	if err != nil {
		return r.deleteError(envReq, session, "INVALID_REQUEST", err.Error(), nil)
	}
	if len(targets) > deleteRequestMaxTargets {
		return r.deleteError(envReq, session, "LIMIT_EXCEEDED", fmt.Sprintf("delete targets exceed maximum %d", deleteRequestMaxTargets), map[string]any{"max_targets": deleteRequestMaxTargets})
	}

	if existing, findErr := r.deletions.FindByIdempotency(ctx, session.ID, principal.ID, key); findErr == nil {
		if !sameDeleteTargetSpec(existing.Manifest.Targets, targets) {
			return r.deleteError(envReq, session, "IDEMPOTENCY_CONFLICT", "idempotency_key is bound to a different delete manifest", map[string]any{"delete_request_id": existing.ID, "manifest_sha256": existing.ManifestSHA256})
		}
		return r.deletePrepareResult(envReq, session, existing, true)
	} else if !errors.Is(findErr, deletion.ErrNotFound) {
		return r.deleteError(envReq, session, "DELETE_STORE_ERROR", findErr.Error(), nil)
	}

	manifest, err := r.freezeDeleteManifest(session, targets)
	if err != nil {
		return r.deleteError(envReq, session, deleteErrorCode(err), deleteErrorMessage(err), deleteErrorDetails(err))
	}
	manifestSHA, err := manifest.SHA256()
	if err != nil {
		return r.deleteError(envReq, session, "DELETE_STORE_ERROR", err.Error(), nil)
	}
	deleteID, idErr := deletion.NewUUID()
	if idErr != nil {
		return r.deleteError(envReq, session, "DELETE_ID_ERROR", idErr.Error(), nil)
	}
	item := deletion.Request{
		ID: deleteID, RemoteSessionID: session.ID, PrincipalID: principal.ID,
		Workspace: session.WorkspaceName, WorkspacePath: session.WorkspacePath, Purpose: envReq.Purpose,
		IdempotencyKey: key, Manifest: manifest, ManifestSHA256: manifestSHA,
	}
	item.ConfirmationUUIDHash = deletion.HashConfirmationUUID(item.ID)
	if err := r.deletions.Create(ctx, item); err != nil {
		if existing, findErr := r.deletions.FindByIdempotency(ctx, session.ID, principal.ID, key); findErr == nil && sameDeleteTargetSpec(existing.Manifest.Targets, targets) {
			return r.deletePrepareResult(envReq, session, existing, true)
		}
		return r.deleteError(envReq, session, "DELETE_STORE_ERROR", err.Error(), nil)
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "workspace.delete.prepared", Summary: fmt.Sprintf("freeze %d delete target(s)", manifest.FileCount), Metadata: map[string]any{"manifest_sha256": manifestSHA, "file_count": manifest.FileCount, "total_bytes": manifest.TotalBytes}})
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "remove_prepare", Status: "prepared", Detail: map[string]any{"delete_request_id": item.ID, "manifest_sha256": manifestSHA, "targets": manifest.Targets, "entry_count": len(manifest.Entries), "file_count": manifest.FileCount, "directory_count": manifest.DirectoryCount, "total_bytes": manifest.TotalBytes, "purpose": envReq.Purpose}})
	return r.deletePrepareResult(envReq, session, item, false)
}

func (r *Runtime) toolWorkspaceDeleteCommit(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	if workspace := strings.TrimSpace(stringPayload(envReq.Payload, "workspace")); workspace != "" && workspace != session.WorkspaceName {
		return r.deleteError(envReq, session, "WORKSPACE_MISMATCH", "workspace does not match the registered remote session", map[string]any{"requested_workspace": workspace, "session_workspace": session.WorkspaceName})
	}
	requestID := strings.TrimSpace(stringPayload(envReq.Payload, "delete_request_id"))
	manifestSHA := strings.TrimSpace(stringPayload(envReq.Payload, "manifest_sha256"))
	key := strings.TrimSpace(stringPayload(envReq.Payload, "idempotency_key"))
	confirmationUUID := strings.TrimSpace(stringPayload(envReq.Payload, "confirmation_uuid"))
	if requestID == "" || manifestSHA == "" || key == "" || confirmationUUID == "" {
		return r.deleteError(envReq, session, "CONFIRMATION_REQUIRED", "confirmation_uuid from remove_prepare is required after the web client obtains user confirmation", map[string]any{"field": "confirmation_uuid", "requires_user_confirmation": true})
	}
	item, err := r.deletions.Get(ctx, requestID)
	if err != nil {
		return r.deleteError(envReq, session, "DELETE_REQUEST_NOT_FOUND", err.Error(), nil)
	}
	if item.RemoteSessionID != session.ID || item.PrincipalID != principal.ID || item.WorkspacePath != session.WorkspacePath || item.Workspace != session.WorkspaceName || item.ManifestSHA256 != manifestSHA || item.IdempotencyKey != key {
		return r.deleteError(envReq, session, "DELETE_MANIFEST_MISMATCH", "delete request is not bound to this session, workspace, manifest or idempotency key", map[string]any{"delete_request_id": requestID})
	}
	if confirmationUUID != requestID || item.ConfirmationUUIDHash == "" || deletion.HashConfirmationUUID(confirmationUUID) != item.ConfirmationUUIDHash {
		return r.deleteError(envReq, session, "CONFIRMATION_MISMATCH", "confirmation_uuid is not the server-issued credential for this delete request", map[string]any{"delete_request_id": requestID, "manifest_sha256": manifestSHA})
	}
	if time.Now().UTC().After(item.ExpiresAt) {
		return r.deleteError(envReq, session, "DELETE_REQUEST_EXPIRED", "delete request approval window has expired", map[string]any{"expires_at": item.ExpiresAt.Format(time.RFC3339Nano)})
	}
	if item.Status == "committed" || item.Status == "partial" || item.Status == "failed" {
		return r.deleteCommitReplay(envReq, session, item)
	}
	if item.Status == "committing" {
		return r.deleteError(envReq, session, "DELETE_IN_PROGRESS", "delete request is being committed; retry with the same idempotency key", map[string]any{"delete_request_id": requestID, "retry_with_same_key": true})
	}
	item, err = r.deletions.MarkCommitting(ctx, requestID)
	if err != nil {
		if errors.Is(err, deletion.ErrInProcess) {
			return r.deleteError(envReq, session, "DELETE_IN_PROGRESS", err.Error(), map[string]any{"delete_request_id": requestID, "retry_with_same_key": true})
		}
		return r.deleteError(envReq, session, "DELETE_REQUEST_CONFLICT", err.Error(), nil)
	}
	result := r.commitDeleteManifest(ctx, envReq, principal, session, item)
	status := "committed"
	if result.FailedCount > 0 {
		status = "partial"
		if result.DeletedCount == 0 {
			status = "failed"
		}
	}
	result.Status = status
	encoded, _ := json.Marshal(result)
	if completeErr := r.deletions.Complete(ctx, requestID, status, encoded); completeErr != nil {
		return r.deleteError(envReq, session, "DELETE_STATE_IN_DOUBT", completeErr.Error(), map[string]any{"delete_request_id": requestID, "manifest_sha256": manifestSHA})
	}
	result.IdempotentReplay = false
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "submit_remove", Status: status, Detail: map[string]any{"delete_request_id": requestID, "manifest_sha256": manifestSHA, "confirmation_uuid_hash": item.ConfirmationUUIDHash, "targets": result.Targets, "deleted_count": result.DeletedCount, "deleted_bytes": result.DeletedBytes, "failed_count": result.FailedCount, "idempotent_replay": result.IdempotentReplay, "audit_event_id": result.AuditEventID}})
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, result)
}

type deleteCommitResult struct {
	Status           string               `json:"status"`
	DeleteRequestID  string               `json:"delete_request_id"`
	ManifestSHA256   string               `json:"manifest_sha256"`
	DeletedCount     int                  `json:"deleted_count"`
	DeletedBytes     int64                `json:"deleted_bytes"`
	FailedCount      int                  `json:"failed_count"`
	Targets          []deleteTargetResult `json:"targets"`
	EditID           string               `json:"edit_id"`
	AuditEventID     string               `json:"audit_event_id"`
	IdempotentReplay bool                 `json:"idempotent_replay,omitempty"`
}

type deleteTargetResult struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Size           int64  `json:"size"`
	Status         string `json:"status"`
	ErrorCode      string `json:"error_code,omitempty"`
}

func (r *Runtime) commitDeleteManifest(ctx context.Context, envReq envelope.Request, principal auth.Principal, session remotesession.Session, item deletion.Request) deleteCommitResult {
	result := deleteCommitResult{DeleteRequestID: item.ID, ManifestSHA256: item.ManifestSHA256, EditID: newRuntimeID("remove", 12), AuditEventID: newRuntimeID("audit", 12), Targets: make([]deleteTargetResult, 0, len(item.Manifest.Entries))}
	current, validationErr := r.freezeDeleteManifest(session, item.Manifest.Targets)
	currentSHA, hashErr := current.SHA256()
	if validationErr != nil || hashErr != nil || currentSHA != item.ManifestSHA256 {
		code := "STALE_REVISION"
		if validationErr != nil {
			code = deleteErrorCode(validationErr)
		}
		for _, target := range item.Manifest.Targets {
			result.Targets = append(result.Targets, deleteTargetResult{Path: target.Path, Kind: target.Kind, ExpectedSHA256: target.ExpectedSHA256, Size: target.Size, Status: "failed", ErrorCode: code})
		}
		result.FailedCount = len(result.Targets)
		result.Status = "failed"
		return result
	}
	root, err := os.OpenRoot(session.WorkspacePath)
	if err != nil {
		for _, target := range current.Entries {
			result.Targets = append(result.Targets, deleteTargetResult{Path: target.Path, Kind: target.Kind, ExpectedSHA256: target.ExpectedSHA256, Size: target.Size, Status: "failed", ErrorCode: "WORKSPACE_ROOT_ERROR"})
		}
		result.FailedCount = len(result.Targets)
		return result
	}
	defer root.Close()
	entries := append([]deletion.Target(nil), current.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		depthI, depthJ := strings.Count(entries[i].Path, "/"), strings.Count(entries[j].Path, "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return entries[i].Path > entries[j].Path
	})
	for start := 0; start < len(entries); start += 64 {
		end := start + 64
		if end > len(entries) {
			end = len(entries)
		}
		for _, target := range entries[start:end] {
			entry := deleteTargetResult{Path: target.Path, Kind: target.Kind, ExpectedSHA256: target.ExpectedSHA256, Size: target.Size}
			if currentErr := validateDeleteEntryForCommit(session.WorkspacePath, target, r.effectiveConfig(session.WorkspacePath)); currentErr != nil {
				entry.Status = "failed"
				entry.ErrorCode = deleteErrorCode(currentErr)
				result.FailedCount++
				result.Targets = append(result.Targets, entry)
				continue
			}
			if removeErr := root.Remove(target.Path); removeErr != nil {
				entry.Status = "failed"
				entry.ErrorCode = "DELETE_FAILED"
				result.FailedCount++
				result.Targets = append(result.Targets, entry)
				continue
			}
			entry.Status = "deleted"
			result.DeletedCount++
			result.DeletedBytes += target.Size
			result.Targets = append(result.Targets, entry)
		}
	}
	if result.FailedCount == 0 {
		result.Status = "committed"
	} else if result.DeletedCount == 0 {
		result.Status = "failed"
	} else {
		result.Status = "partial"
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: session.ID, Type: "workspace.remove.committed", OperationID: result.EditID, Summary: fmt.Sprintf("removed %d target(s), %d bytes", result.DeletedCount, result.DeletedBytes), Metadata: map[string]any{"delete_request_id": item.ID, "manifest_sha256": item.ManifestSHA256, "status": result.Status, "audit_event_id": result.AuditEventID}})
	r.observeWorkspaceDelete(ctx, envReq, session, result)
	return result
}

func (r *Runtime) observeWorkspaceDelete(ctx context.Context, envReq envelope.Request, session remotesession.Session, result deleteCommitResult) {
	if r.observation == nil {
		return
	}
	payload, _ := json.Marshal(result)
	_ = r.observation.Record(ctx, observation.Event{Workspace: session.WorkspaceName, RemoteSessionID: session.ID, RequestID: envReq.RequestID, CallID: observationCallID(envReq), Tool: "submit_remove", Type: observation.TypeFileChanged, Status: result.Status, Purpose: envReq.Purpose, Intent: envReq.Intent, Output: payload, Summary: fmt.Sprintf("removed %d target(s), %d bytes", result.DeletedCount, result.DeletedBytes)})
}

func (r *Runtime) deletePrepareResult(envReq envelope.Request, session remotesession.Session, item deletion.Request, replay bool) (*mcp.CallToolResult, error) {
	submitArguments := map[string]any{
		"remote_session_id": session.ID,
		"workspace":         item.Workspace,
		"purpose":           item.Purpose,
		"delete_request_id": item.ID,
		"manifest_sha256":   item.ManifestSHA256,
		"confirmation_uuid": item.ID,
		"idempotency_key":   item.IdempotencyKey,
	}
	data := map[string]any{
		"summary":                    "删除清单已冻结；用户确认后使用 submit_remove_arguments 提交",
		"delete_request_id":          item.ID,
		"confirmation_uuid":          item.ID,
		"manifest_sha256":            item.ManifestSHA256,
		"idempotency_key":            item.IdempotencyKey,
		"workspace":                  item.Workspace,
		"targets":                    item.Manifest.Targets,
		"entries":                    item.Manifest.Entries,
		"entry_count":                len(item.Manifest.Entries),
		"file_count":                 item.Manifest.FileCount,
		"directory_count":            item.Manifest.DirectoryCount,
		"total_bytes":                item.Manifest.TotalBytes,
		"expires_at":                 item.ExpiresAt.Format(time.RFC3339Nano),
		"requires_user_confirmation": true,
		"approval_surface":           "web_model_user_question",
		"submit_remove_arguments":    submitArguments,
		"filesystem_mutated":         false,
	}
	if replay {
		data["idempotent_replay"] = true
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
}

func (r *Runtime) deleteCommitReplay(envReq envelope.Request, session remotesession.Session, item deletion.Request) (*mcp.CallToolResult, error) {
	var result deleteCommitResult
	if err := json.Unmarshal(item.ResultJSON, &result); err != nil {
		return r.deleteError(envReq, session, "DELETE_STATE_IN_DOUBT", err.Error(), map[string]any{"delete_request_id": item.ID})
	}
	result.IdempotentReplay = true
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, result)
}

func parseDeleteTargets(payload map[string]any) ([]deletion.Target, error) {
	raw, ok := payload["targets"]
	if !ok {
		return nil, errors.New("targets is required")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid targets: %w", err)
	}
	var targets []deletion.Target
	if err := json.Unmarshal(b, &targets); err != nil || len(targets) == 0 {
		return nil, errors.New("targets must contain at least one path, kind and expected_sha256")
	}
	seen := map[string]bool{}
	for i := range targets {
		canonical, err := canonicalDeletePath(targets[i].Path)
		if err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
		targets[i].Path = canonical
		switch targets[i].Kind {
		case "file":
			if targets[i].ExpectedSHA256 == "" {
				return nil, fmt.Errorf("targets[%d].expected_sha256 is required for file", i)
			}
		case "directory":
		default:
			return nil, fmt.Errorf("targets[%d].kind must be file or directory", i)
		}
		targets[i].ExpectedSHA256 = normalizeSHA(targets[i].ExpectedSHA256)
		if seen[canonical] {
			return nil, fmt.Errorf("duplicate delete target %q", canonical)
		}
		seen[canonical] = true
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	for i := 1; i < len(targets); i++ {
		if strings.HasPrefix(targets[i].Path, targets[i-1].Path+"/") {
			return nil, fmt.Errorf("overlapping delete targets are not allowed: %q contains %q", targets[i-1].Path, targets[i].Path)
		}
	}
	return targets, nil
}

func (r *Runtime) freezeDeleteManifest(session remotesession.Session, targets []deletion.Target) (deletion.Manifest, error) {
	manifest := deletion.Manifest{Workspace: session.WorkspaceName, Targets: make([]deletion.Target, 0, len(targets)), Entries: make([]deletion.Target, 0)}
	for _, target := range targets {
		if err := validateFrozenDeleteTarget(session.WorkspacePath, target, r.effectiveConfig(session.WorkspacePath)); err != nil {
			return deletion.Manifest{}, err
		}
		absolute, err := file.Resolve(session.WorkspacePath, target.Path)
		if err != nil {
			return deletion.Manifest{}, &deleteValidationError{Code: "PATH_ESCAPE", Message: err.Error(), Path: target.Path}
		}
		if target.Kind == "directory" {
			nested, total, actual, walkErr := freezeDeleteDirectory(session.WorkspacePath, target, r.effectiveConfig(session.WorkspacePath))
			if walkErr != nil {
				return deletion.Manifest{}, walkErr
			}
			if target.ExpectedSHA256 != "" && target.ExpectedSHA256 != actual {
				return deletion.Manifest{}, &deleteValidationError{Code: "STALE_REVISION", Message: "expected_sha256 does not match current directory tree", Path: target.Path, CurrentSHA: actual}
			}
			copyTarget := target
			copyTarget.ExpectedSHA256, copyTarget.Size = actual, total
			manifest.Targets = append(manifest.Targets, copyTarget)
			manifest.Entries = append(manifest.Entries, nested...)
			manifest.Entries = append(manifest.Entries, copyTarget)
			manifest.TotalBytes += total
			continue
		}
		content, readErr := os.ReadFile(absolute)
		if readErr != nil {
			return deletion.Manifest{}, &deleteValidationError{Code: "FILE_NOT_FOUND", Message: "file not found", Path: target.Path}
		}
		actual := digestBytes(content)
		if actual != target.ExpectedSHA256 {
			return deletion.Manifest{}, &deleteValidationError{Code: "STALE_REVISION", Message: "expected_sha256 does not match current file", Path: target.Path, CurrentSHA: actual}
		}
		copyTarget := target
		copyTarget.Size = int64(len(content))
		manifest.Targets = append(manifest.Targets, copyTarget)
		manifest.Entries = append(manifest.Entries, copyTarget)
		manifest.TotalBytes += copyTarget.Size
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	if len(manifest.Entries) > MaxRemoveManifestEntries {
		return deletion.Manifest{}, &deleteValidationError{Code: "LIMIT_EXCEEDED", Message: fmt.Sprintf("delete manifest exceeds maximum %d entries", MaxRemoveManifestEntries), Path: targets[0].Path}
	}
	for _, entry := range manifest.Entries {
		if entry.Kind == "file" {
			manifest.FileCount++
		} else {
			manifest.DirectoryCount++
		}
	}
	return manifest, nil
}

func freezeDeleteDirectory(workspacePath string, target deletion.Target, cfg config.Config) ([]deletion.Target, int64, string, error) {
	root, err := file.LexicalPath(workspacePath, target.Path)
	if err != nil {
		return nil, 0, "", &deleteValidationError{Code: "PATH_ESCAPE", Message: err.Error(), Path: target.Path}
	}
	entries := make([]deletion.Target, 0)
	var total int64
	err = filepath.WalkDir(root, func(path string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(workspacePath, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		info, infoErr := dirEntry.Info()
		if infoErr != nil {
			return infoErr
		}
		if security.MatchFile(cfg.Security.Files, relative) == security.Deny {
			return &deleteValidationError{Code: "FILE_DENIED", Message: "file denied by policy", Path: relative}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &deleteValidationError{Code: "SYMLINK_NOT_ALLOWED", Message: "directory manifest cannot follow symlink", Path: relative}
		}
		if dirEntry.IsDir() {
			entries = append(entries, deletion.Target{Path: relative, Kind: "directory"})
			return nil
		}
		if !info.Mode().IsRegular() {
			return &deleteValidationError{Code: "DELETE_FILE_ONLY", Message: "directory manifest accepts regular files and directories only", Path: relative}
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sha := digestBytes(content)
		entries = append(entries, deletion.Target{Path: relative, Kind: "file", ExpectedSHA256: sha, Size: int64(len(content))})
		total += int64(len(content))
		return nil
	})
	if err != nil {
		if validation, ok := err.(*deleteValidationError); ok {
			return nil, 0, "", validation
		}
		return nil, 0, "", &deleteValidationError{Code: "DIRECTORY_READ_FAILED", Message: err.Error(), Path: target.Path}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	canonical := append([]deletion.Target{{Path: target.Path, Kind: "directory"}}, entries...)
	hash := sha256.New()
	for _, entry := range canonical {
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%d\n", entry.Kind, entry.Path, entry.ExpectedSHA256, entry.Size)
	}
	return entries, total, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func validateFrozenDeleteTarget(workspacePath string, target deletion.Target, cfg config.Config) error {
	canonical, err := canonicalDeletePath(target.Path)
	if err != nil || canonical != target.Path {
		return &deleteValidationError{Code: "INVALID_PATH", Message: "delete target must be a canonical workspace-relative path", Path: target.Path}
	}
	if security.MatchFile(cfg.Security.Files, target.Path) == security.Deny {
		return &deleteValidationError{Code: "FILE_DENIED", Message: "file denied by policy", Path: target.Path}
	}
	lexical, err := file.LexicalPath(workspacePath, target.Path)
	if err != nil {
		return &deleteValidationError{Code: "PATH_ESCAPE", Message: err.Error(), Path: target.Path}
	}
	if err := rejectSymlinkComponents(workspacePath, target.Path); err != nil {
		return err
	}
	info, err := os.Lstat(lexical)
	if err != nil {
		return &deleteValidationError{Code: "FILE_NOT_FOUND", Message: "file not found", Path: target.Path}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &deleteValidationError{Code: "SYMLINK_NOT_ALLOWED", Message: "symlink deletion is not allowed", Path: target.Path}
	}
	if target.Kind == "directory" {
		if !info.IsDir() {
			return &deleteValidationError{Code: "DELETE_DIRECTORY_ONLY", Message: "target is not a directory", Path: target.Path}
		}
		return nil
	}
	if target.Kind != "file" || !info.Mode().IsRegular() {
		return &deleteValidationError{Code: "DELETE_FILE_ONLY", Message: "only regular files or explicitly requested directories can be deleted", Path: target.Path}
	}
	return nil
}

func rejectSymlinkComponents(workspacePath, relativePath string) error {
	current := workspacePath
	for _, component := range strings.Split(filepath.ToSlash(relativePath), "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return &deleteValidationError{Code: "PATH_STAT_FAILED", Message: err.Error(), Path: relativePath}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &deleteValidationError{Code: "SYMLINK_NOT_ALLOWED", Message: "delete path cannot contain symlink components", Path: relativePath}
		}
	}
	return nil
}

func validateDeleteEntryForCommit(workspacePath string, target deletion.Target, cfg config.Config) error {
	if err := validateFrozenDeleteTarget(workspacePath, target, cfg); err != nil {
		return err
	}
	lexical, err := file.LexicalPath(workspacePath, target.Path)
	if err != nil {
		return &deleteValidationError{Code: "PATH_ESCAPE", Message: err.Error(), Path: target.Path}
	}
	if target.Kind == "directory" {
		entries, readErr := os.ReadDir(lexical)
		if readErr != nil {
			return &deleteValidationError{Code: "DIRECTORY_READ_FAILED", Message: readErr.Error(), Path: target.Path}
		}
		if len(entries) != 0 {
			return &deleteValidationError{Code: "DIRECTORY_CHANGED", Message: "directory is not empty at removal time", Path: target.Path}
		}
		return nil
	}
	content, readErr := os.ReadFile(lexical)
	if readErr != nil {
		return &deleteValidationError{Code: "FILE_READ_FAILED", Message: readErr.Error(), Path: target.Path}
	}
	actual := digestBytes(content)
	if actual != target.ExpectedSHA256 {
		return &deleteValidationError{Code: "STALE_REVISION", Message: "expected_sha256 does not match current file", Path: target.Path, CurrentSHA: actual}
	}
	return nil
}

type deleteValidationError struct {
	Code, Message, Path, CurrentSHA string
}

func (e *deleteValidationError) Error() string { return e.Message }

func deleteErrorCode(err error) string {
	var validation *deleteValidationError
	if errors.As(err, &validation) {
		return validation.Code
	}
	return "DELETE_FAILED"
}

func deleteErrorMessage(err error) string {
	var validation *deleteValidationError
	if errors.As(err, &validation) {
		return validation.Message
	}
	return err.Error()
}

func deleteErrorDetails(err error) map[string]any {
	var validation *deleteValidationError
	if !errors.As(err, &validation) {
		return nil
	}
	details := map[string]any{"path": validation.Path}
	if validation.CurrentSHA != "" {
		details["current_sha256"] = validation.CurrentSHA
	}
	return details
}

func (r *Runtime) deleteError(envReq envelope.Request, session remotesession.Session, code, message string, details map[string]any) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, code, message)
	response.RemoteSessionID = session.ID
	for key, value := range details {
		response.Error.Details[key] = value
	}
	return r.resultJSON(response)
}

func canonicalDeletePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("path must be a non-empty relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must remain inside the workspace")
	}
	return clean, nil
}

func normalizeSHA(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "sha256:") {
		return value
	}
	if len(value) == sha256.Size*2 {
		return "sha256:" + value
	}
	return value
}

func digestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sameDeleteTargetSpec(existing, requested []deletion.Target) bool {
	if len(existing) != len(requested) {
		return false
	}
	for i := range existing {
		if existing[i].Path != requested[i].Path || existing[i].Kind != requested[i].Kind {
			return false
		}
		if requested[i].Kind == "directory" && requested[i].ExpectedSHA256 == "" {
			continue
		}
		if existing[i].ExpectedSHA256 != requested[i].ExpectedSHA256 {
			return false
		}
	}
	return true
}
