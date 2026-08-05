package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
	"mcpx/internal/workspacechanges"
)

var (
	errWorkspaceNotFound     = errors.New("workspace not found")
	errRemoteSessionRequired = errors.New("remote session id required")
	errRemoteSessionRunning  = errors.New("remote session has running tasks")
)

func (r *Runtime) principalFromContext(ctx context.Context) (auth.Principal, error) {
	issuer, resource := r.authAudience()
	credentials := auth.ValidateHTTP(
		bearerFromCtx(ctx), config.EffectiveAuthMode(r.cfg.Auth), r.cfg.Auth.Token,
		r.oauth, issuer, resource,
	)
	if !credentials.OK {
		return auth.Principal{}, fmt.Errorf("unauthorized")
	}
	return auth.PrincipalFromCredentials(credentials, bearerFromCtx(ctx)), nil
}

func clientInfoFromContext(ctx context.Context) (name, version string) {
	if session := mcpserver.ClientSessionFromContext(ctx); session != nil {
		if withInfo, ok := session.(mcpserver.SessionWithClientInfo); ok {
			info := withInfo.GetClientInfo()
			return info.Name, info.Version
		}
	}
	return "unknown", ""
}

func (r *Runtime) remoteRequest(ctx context.Context, req mcp.CallToolRequest) (envelope.Request, auth.Principal, *mcp.CallToolResult) {
	envReq, err := r.parseEnv(ctx, req)
	if err != nil {
		return envReq, auth.Principal{}, mcp.NewToolResultError(err.Error())
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		resp := envelope.Fail(envelope.StatusUnauthorized, envReq.RequestID, envReq.Workspace, nil, "unauthorized", "invalid or missing token")
		out, _ := r.resultJSON(resp)
		return envReq, auth.Principal{}, out
	}
	return envReq, principal, nil
}

func validatePurpose(purpose string) error {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		return fmt.Errorf("purpose is required")
	}
	if len(purpose) > envelope.MaxIntentBytes {
		return fmt.Errorf("purpose exceeds %d bytes", envelope.MaxIntentBytes)
	}
	return nil
}

// validateIntent is kept for internal callers that still use the observation
// field name; public schemas expose purpose.
func validateIntent(intent string) error { return validatePurpose(intent) }

func (r *Runtime) remoteResult(envReq envelope.Request, remoteSessionID, workspace string, data any) (*mcp.CallToolResult, error) {
	resp := envelope.OK(envReq.RequestID, workspace, data)
	resp.RemoteSessionID = remoteSessionID
	return r.resultJSON(resp)
}

func (r *Runtime) remoteError(envReq envelope.Request, remoteSessionID, workspace string, err error) (*mcp.CallToolResult, error) {
	status, code := envelope.StatusError, "remote_session_error"
	switch {
	case errors.Is(err, remotesession.ErrNotFound):
		code = "not_found"
	case errors.Is(err, remotesession.ErrForbidden):
		status, code = envelope.StatusDenied, "forbidden"
	case errors.Is(err, remotesession.ErrConflict):
		code = "version_conflict"
	case errors.Is(err, errRemoteSessionRunning):
		code = "running_task"
	case errors.Is(err, remotesession.ErrInvalidToken):
		status, code = envelope.StatusDenied, "invalid_handoff_token"
	case errors.Is(err, remotesession.ErrInvalidInput):
		code = "invalid_request"
	case errors.Is(err, errWorkspaceNotFound):
		code = "workspace_not_found"
	case errors.Is(err, errRemoteSessionRequired):
		code = "remote_session_required"
	}
	message := err.Error()
	if code == "not_found" {
		message = "remote session not found：session_id 必须原样复制 session_open 返回的完整值，不能改写、缩写或凭记忆重输。"
	}
	resp := envelope.Fail(status, envReq.RequestID, workspace, nil, code, message)
	resp.RemoteSessionID = remoteSessionID
	switch code {
	case "workspace_not_found":
		addRecoveryAction(&resp, "workspace_list", "select a valid workspace before retrying session_open", nil)
		addRecoveryActions(&resp,
			nextActionWithReason("workspace_list", "refresh the available workspace names", nil),
			nextActionWithReason("session_open", "open a Remote Session after selecting a workspace", map[string]any{"workspace": workspace}),
		)
	case "not_found":
		if remoteSessionID != "" {
			addRecoveryAction(&resp, "workspace_list", "refresh workspace selection before opening a new Remote Session", nil)
		}
	case "remote_session_required":
		if workspace != "" {
			addRecoveryAction(&resp, "session_open", "open or resume a Remote Session before using this tool", map[string]any{"workspace": workspace})
		} else {
			addRecoveryAction(&resp, "workspace_list", "select a workspace before opening a Remote Session", nil)
		}
	}
	return r.resultJSON(resp)
}

func (r *Runtime) createRemoteSession(ctx context.Context, principal auth.Principal, envReq envelope.Request, workspaceName string) (remotesession.CreateResult, error) {
	workspaceName = strings.TrimSpace(workspaceName)
	ws, ok := r.reg.Get(workspaceName)
	if !ok {
		return remotesession.CreateResult{}, fmt.Errorf("%w: %q", errWorkspaceNotFound, workspaceName)
	}
	label, _ := envReq.Payload["label"].(string)
	description, _ := envReq.Payload["description"].(string)
	clientRequestID, _ := envReq.Payload["client_request_id"].(string)
	clientName, clientVersion := clientInfoFromContext(ctx)
	gitHead, treeDigest := workspaceRevision(ctx, ws.Path)
	result, err := r.remote.Create(ctx, principal, remotesession.CreateInput{
		WorkspaceName: workspaceName, WorkspacePath: ws.Path, Label: label,
		Description: description, BaseGitHead: gitHead, BaseTreeDigest: treeDigest,
		ClientRequestID: clientRequestID, ClientName: clientName, ClientVersion: clientVersion,
	})
	if err != nil {
		return remotesession.CreateResult{}, err
	}
	if err := r.ensureSessionEnvironment(ctx, principal, &result); err != nil {
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: result.Session.ID, Workspace: workspaceName, Tool: "environment_snapshot", Status: "error", Detail: map[string]any{"error": err.Error()}})
	}
	if result.ResumeTokenAlreadyIssued {
		return result, nil
	}
	if err := r.workspaceDiff.CaptureBaseline(ctx, result.Session.ID, result.Session.WorkspacePath); err != nil {
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: result.Session.ID, Workspace: workspaceName, Tool: "workspace_baseline", Status: "error", Detail: map[string]any{"error": err.Error()}})
	}
	return result, nil
}

func (r *Runtime) toolRemoteSessionList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	workspaceName := strings.TrimSpace(envReq.Workspace)
	if workspaceName == "" {
		workspaceName, _ = envReq.Payload["workspace"].(string)
	}
	query, _ := envReq.Payload["query"].(string)
	status, _ := envReq.Payload["status"].(string)
	cursor, _ := envReq.Payload["cursor"].(string)
	limit := intPayload(envReq.Payload, "limit")
	var statuses []string
	for _, value := range strings.Split(status, ",") {
		if value = strings.TrimSpace(value); value != "" {
			statuses = append(statuses, value)
		}
	}
	result, err := r.remote.List(ctx, principal, remotesession.ListInput{Workspace: workspaceName, Query: query, Statuses: statuses, Limit: limit, Cursor: cursor})
	if err != nil {
		return r.remoteError(envReq, "", workspaceName, err)
	}
	return r.remoteResult(envReq, "", workspaceName, result)
}

func (r *Runtime) toolRemoteSessionGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	remoteSessionID, err := requireRemoteSessionID(envReq)
	if err != nil {
		return r.remoteError(envReq, "", "", err)
	}
	session, err := r.remote.Get(ctx, principal, remoteSessionID)
	if err != nil {
		return r.remoteError(envReq, remoteSessionID, "", err)
	}
	encoded, _ := json.Marshal(session)
	data := map[string]any{}
	_ = json.Unmarshal(encoded, &data)
	if tasks, taskErr := r.tasks.List(session.ID, 20); taskErr == nil {
		data["tasks"] = tasks
	}
	if history, historyErr := r.changesets.History(ctx, session.ID, 10); historyErr == nil {
		data["recent_changesets"] = history
	}
	if session.Role == "owner" || session.Role == "approver" {
		data["pending_confirmations"] = pendingConfirmationItems(r.approvals.ListRemoteSession(session.ID))
	}
	if artifacts, artifactErr := r.artifacts.List(ctx, session.ID, "", 10); artifactErr == nil {
		data["recent_artifacts"] = artifacts
	}
	return r.remoteResult(envReq, remoteSessionID, session.WorkspaceName, data)
}

func (r *Runtime) toolRemoteSessionEvents(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	remoteSessionID, err := requireRemoteSessionID(envReq)
	if err != nil {
		return r.remoteError(envReq, "", "", err)
	}
	events, err := r.remote.Events(ctx, principal, remoteSessionID, remotesession.EventsInput{AfterSequence: int64(intPayload(envReq.Payload, "after_sequence")), Limit: intPayload(envReq.Payload, "limit")})
	if err != nil {
		return r.remoteError(envReq, remoteSessionID, "", err)
	}
	data := map[string]any{"events": events}
	// Models recovering a confirmed command from the event log also need the
	// pending confirmation token; otherwise a lost retry token is unrecoverable.
	if session, getErr := r.remote.Get(ctx, principal, remoteSessionID); getErr == nil && (session.Role == "owner" || session.Role == "approver") {
		if pending := r.approvals.ListRemoteSession(session.ID); len(pending) > 0 {
			data["pending_confirmations"] = pendingConfirmationItems(pending)
		}
	}
	return r.remoteResult(envReq, remoteSessionID, "", data)
}

func (r *Runtime) toolRemoteSessionUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	remoteSessionID, err := requireRemoteSessionID(envReq)
	if err != nil {
		return r.remoteError(envReq, "", "", err)
	}
	label, _ := envReq.Payload["label"].(string)
	description, _ := envReq.Payload["description"].(string)
	status, _ := envReq.Payload["status"].(string)
	session, err := r.remote.Update(ctx, principal, remoteSessionID, label, description, status, intPayload(envReq.Payload, "expected_version"))
	if err != nil {
		return r.remoteError(envReq, remoteSessionID, "", err)
	}
	return r.remoteResult(envReq, remoteSessionID, session.WorkspaceName, session)
}

func (r *Runtime) toolRemoteSessionHandoff(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	remoteSessionID, err := requireRemoteSessionID(envReq)
	if err != nil {
		return r.remoteError(envReq, "", "", err)
	}
	role, _ := envReq.Payload["role"].(string)
	note, _ := envReq.Payload["note"].(string)
	result, err := r.remote.Handoff(ctx, principal, remoteSessionID, role, note, time.Duration(intPayload(envReq.Payload, "expires_in"))*time.Second)
	if err != nil {
		return r.remoteError(envReq, remoteSessionID, "", err)
	}
	return r.remoteResult(envReq, remoteSessionID, "", result)
}

func (r *Runtime) toolRemoteSessionAttach(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	token, _ := envReq.Payload["handoff_token"].(string)
	clientName, clientVersion := clientInfoFromContext(ctx)
	session, err := r.remote.Attach(ctx, principal, token, clientName, clientVersion)
	if err != nil {
		return r.remoteError(envReq, "", "", err)
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{
		"session":                session,
		"recommended_next_calls": []string{"session_open", "session_read", "workspace_observe", "task_read"},
	})
}

func (r *Runtime) toolRemoteSessionClose(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	remoteSessionID, err := requireRemoteSessionID(envReq)
	if err != nil {
		return r.remoteError(envReq, "", "", err)
	}
	if tasks, taskErr := r.tasks.List(remoteSessionID, 100); taskErr == nil {
		for _, task := range tasks {
			if fmt.Sprint(task["status"]) == "running" {
				return r.remoteError(envReq, remoteSessionID, "", fmt.Errorf("%w: task %v must be stopped before closing", errRemoteSessionRunning, task["task_id"]))
			}
		}
	}
	mode, _ := envReq.Payload["mode"].(string)
	session, err := r.remote.Close(ctx, principal, remoteSessionID, mode)
	if err != nil {
		return r.remoteError(envReq, remoteSessionID, "", err)
	}
	return r.remoteResult(envReq, remoteSessionID, session.WorkspaceName, session)
}

func remoteSessionID(req envelope.Request) string {
	if req.RemoteSessionID != "" {
		return req.RemoteSessionID
	}
	value, _ := req.Payload["remote_session_id"].(string)
	return strings.TrimSpace(value)
}

func requireRemoteSessionID(req envelope.Request) (string, error) {
	id := remoteSessionID(req)
	if id == "" {
		return "", errRemoteSessionRequired
	}
	return id, nil
}

func intPayload(payload map[string]any, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func workspaceRevision(parent context.Context, path string) (head, digest string) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	roots, err := workspacechanges.DiscoverGitRoots(path)
	if err != nil || len(roots) == 0 {
		return "", ""
	}
	heads := make([]string, 0, len(roots))
	var statusParts []string
	for _, root := range roots {
		rootPath := path
		label := ""
		if root != "" {
			rootPath = filepath.Join(path, root)
			label = root + ":"
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", rootPath, "rev-parse", "HEAD").Output(); err == nil {
			heads = append(heads, label+strings.TrimSpace(string(out)))
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", rootPath, "status", "--porcelain=v2", "-z").Output(); err == nil {
			statusParts = append(statusParts, label+string(out))
		}
	}
	if len(heads) == 0 {
		return "", ""
	}
	head = strings.Join(heads, " ")
	digest = remotesessionDigest(head + "\x00" + strings.Join(statusParts, "\x00"))
	return head, digest
}

func remotesessionDigest(value string) string {
	// Reuse the standard library without exposing workspace contents.
	return fmt.Sprintf("sha256:%x", sha256Sum([]byte(value)))
}

func sha256Sum(value []byte) [32]byte {
	return sha256.Sum256(value)
}
