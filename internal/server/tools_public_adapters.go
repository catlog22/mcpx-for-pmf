package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/observation"
)

func publicSelector(req *mcp.CallToolRequest, key string) string {
	value, _ := mcpresult.Arguments(req)[key].(string)
	return strings.ToLower(strings.TrimSpace(value))
}

func publicDispatch(req *mcp.CallToolRequest, key, value string) *mcp.CallToolRequest {
	return forwardedRequest(req, map[string]any{key: value})
}

func (r *Runtime) toolWorkspaceObserve(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	view := publicSelector(req, "view")
	switch view {
	case "changes":
		return r.toolObserveChanges(ctx, req)
	case "history":
		return r.toolWorkspaceHistoryRead(ctx, req)
	default:
		return r.toolWorkspaceState(ctx, publicDispatch(req, "action", view))
	}
}

// toolWorkspaceRead consolidates list/observe/history behind view.
func (r *Runtime) toolWorkspaceRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch publicSelector(req, "view") {
	case "list":
		return r.toolWorkspaceList(ctx, req)
	case "history":
		return r.toolWorkspaceHistoryRead(ctx, req)
	default:
		return r.toolWorkspaceObserve(ctx, req)
	}
}

// toolSession consolidates open + lifecycle transitions behind action.
func (r *Runtime) toolSession(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	action := publicSelector(req, "action")
	switch action {
	case "open":
		return r.toolSessionOpen(ctx, req)
	case "attach":
		// Clean core handoff is direct session reuse. The caller supplies the
		// existing remote_session_id; no one-time handoff token is involved.
		return r.toolSessionOpen(ctx, req)
	case "close":
		return r.toolSessionTransition(ctx, publicDispatch(req, "operation", action))
	default:
		envReq, _, fail := r.remoteRequest(ctx, req)
		if fail != nil {
			return fail, nil
		}
		return r.terminalError(envReq, envReq.RemoteSessionID, envReq.Workspace, "bad_request", "action must be open, close, or attach")
	}
}

// toolTask consolidates attach/stop/stdin behind action.
func (r *Runtime) toolTask(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolTaskControl(ctx, publicDispatch(req, "operation", publicSelector(req, "action")))
}

// toolPlan consolidates create + transitions behind action.
func (r *Runtime) toolPlan(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := publicSelector(req, "action")
	if action == "create" {
		return r.toolPlanCreatePublic(ctx, req)
	}
	return r.toolPlanTransition(ctx, publicDispatch(req, "transition", action))
}

// toolEnvironment is write-side snapshot create (action=snapshot_create).
func (r *Runtime) toolEnvironment(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if action := publicSelector(req, "action"); action != "" && action != "snapshot_create" {
		envReq, _, remote, fail := r.changeRequest(ctx, req, false)
		if fail != nil {
			return fail, nil
		}
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "action must be snapshot_create")
	}
	return r.toolEnvironmentSnapshotCreate(ctx, req)
}

// toolArtifact is write-side register.
func (r *Runtime) toolArtifact(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolArtifactRegister(ctx, req)
}

func (r *Runtime) toolWorkspaceHistoryRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	if r.observation == nil || r.observation.store == nil {
		return r.terminalError(envReq, "", envReq.Workspace, "history_unavailable", "workspace history is unavailable")
	}
	resolvedWorkspace, remoteID, resolveErr := r.resolveExplicitWorkspace(ctx, principal, envReq)
	if resolveErr != nil {
		return r.terminalError(envReq, remoteID, envReq.Workspace, "workspace_required", resolveErr.Error())
	}
	workspaceName := resolvedWorkspace.Name
	if workspaceName == "" {
		return r.terminalError(envReq, remoteID, "", "workspace_required", "workspace is required for history query")
	}
	query := observation.HistoryQuery{
		Workspace:        workspaceName,
		SessionID:        firstString(envReq.RemoteSessionID, stringPayload(envReq.Payload, "session_id")),
		CallID:           firstString(envReq.CallID, stringPayload(envReq.Payload, "call_id")),
		EventIDs:         stringSlicePayload(envReq.Payload, "event_ids"),
		RequestIDs:       append(stringSlicePayload(envReq.Payload, "request_ids"), stringPayload(envReq.Payload, "request_id")),
		OperationIDs:     stringSlicePayload(envReq.Payload, "operation_ids"),
		PlanTaskIDs:      append(stringSlicePayload(envReq.Payload, "plan_task_ids"), stringPayload(envReq.Payload, "plan_task_id")),
		ExecutionTaskIDs: append(stringSlicePayload(envReq.Payload, "execution_task_ids"), stringPayload(envReq.Payload, "execution_task_id")),
		Keyword:          stringPayload(envReq.Payload, "keyword"),
		Kinds:            stringSlicePayload(envReq.Payload, "kinds"),
		Statuses:         stringSlicePayload(envReq.Payload, "statuses"),
		Limit:            intPayload(envReq.Payload, "limit"),
		Cursor:           stringPayload(envReq.Payload, "cursor"),
	}
	var parseErr error
	query.CreatedAfter, parseErr = historyTimePayload(envReq.Payload, "created_after")
	if parseErr != nil {
		return r.terminalError(envReq, remoteID, workspaceName, "history_invalid_time", parseErr.Error())
	}
	query.CreatedBefore, parseErr = historyTimePayload(envReq.Payload, "created_before")
	if parseErr != nil {
		return r.terminalError(envReq, remoteID, workspaceName, "history_invalid_time", parseErr.Error())
	}
	events, nextCursor, err := r.observation.store.Query(ctx, query)
	if err != nil {
		return r.terminalError(envReq, remoteID, workspaceName, "history_query_error", err.Error())
	}
	views := make([]map[string]any, 0, len(events))
	for _, event := range events {
		views = append(views, historyEventView(event))
	}
	return r.remoteResult(envReq, remoteID, workspaceName, map[string]any{"workspace": workspaceName, "events": views, "next_cursor": nextCursor, "count": len(views)})
}

func historyEventView(event observation.Event) map[string]any {
	view := map[string]any{
		"event_id": event.EventID, "sequence": event.Sequence, "workspace": event.Workspace,
		"session_id": event.RemoteSessionID, "request_id": event.RequestID, "call_id": event.CallID, "operation_id": event.OperationID,
		"parent_operation_id": event.ParentOperationID, "step_id": event.StepID, "kind": event.Type, "type": event.Type,
		"name": event.Tool, "tool": event.Tool, "phase": event.Phase, "status": event.Status,
		"goal": event.Goal, "purpose": event.Purpose, "reasoning_summary": event.ReasoningSummary,
		"progress_summary": event.ProgressSummary, "next_step": event.NextStep, "plan_id": event.PlanID,
		"plan_task_id": event.PlanTaskID, "execution_task_id": event.ExecutionTaskID, "summary": event.Summary, "command": event.Command,
		"working_directory": event.WorkingDirectory, "duration_ms": event.DurationMs,
		"skill_name": event.SkillName, "mcp_server": event.MCPServer, "mcp_tool": event.MCPTool,
		"path": event.Path, "resource_uri": event.ResourceURI, "stream": event.Stream,
		"offset": event.Offset, "truncated": event.Truncated, "created_at": event.CreatedAt,
	}
	if event.ExitCode != nil {
		view["exit_code"] = *event.ExitCode
	}
	return view
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stringSlicePayload(payload map[string]any, key string) []string {
	value, ok := payload[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func historyTimePayload(payload map[string]any, key string) (time.Time, error) {
	value := strings.TrimSpace(stringPayload(payload, key))
	if value == "" {
		return time.Time{}, nil
	}
	if day, err := time.Parse("2006-01-02", value); err == nil {
		day = day.UTC()
		if key == "created_before" {
			return day.Add(24*time.Hour - time.Nanosecond), nil
		}
		return day, nil
	}
	if milliseconds, err := parseUnixMillis(value); err == nil {
		return time.UnixMilli(milliseconds).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q; use RFC3339, YYYY-MM-DD or Unix milliseconds", key, value)
	}
	return parsed.UTC(), nil
}

func parseUnixMillis(value string) (int64, error) {
	var parsed int64
	if _, err := fmt.Sscan(value, &parsed); err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid timestamp")
	}
	return parsed, nil
}

func (r *Runtime) toolSessionRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := publicSelector(req, "view")
	if action == "summary" {
		action = "get"
	}
	return r.toolSessionManage(ctx, publicDispatch(req, "action", action))
}

func (r *Runtime) toolSessionTransition(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolSessionManage(ctx, publicDispatch(req, "action", publicSelector(req, "operation")))
}

func (r *Runtime) toolSourceRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	view := publicSelector(req, "view")
	if view == "file" {
		return r.toolFileReadUnified(ctx, req)
	}
	action := view
	if action == "context" {
		action = "query"
	}
	updates := map[string]any{"action": action}
	if mode, ok := mcpresult.Arguments(req)["search_mode"]; ok {
		updates["mode"] = mode
	}
	return r.toolContextQueryUnified(ctx, forwardedRequest(req, updates))
}

func (r *Runtime) toolCommandRun(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolCommandExecute(ctx, req)
}

func (r *Runtime) toolTaskRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolTaskManage(ctx, publicDispatch(req, "action", publicSelector(req, "view")))
}

func (r *Runtime) toolTaskControl(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolTaskManage(ctx, publicDispatch(req, "action", publicSelector(req, "operation")))
}

func (r *Runtime) toolPlanCreatePublic(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolPlanManage(ctx, publicDispatch(req, "action", "create"))
}

func (r *Runtime) toolPlanReadPublic(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolPlanManage(ctx, publicDispatch(req, "action", "get"))
}

func (r *Runtime) toolPlanTransition(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolPlanManage(ctx, publicDispatch(req, "action", publicSelector(req, "transition")))
}

func (r *Runtime) toolRuntimeRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolRuntimeInspect(ctx, publicDispatch(req, "action", publicSelector(req, "view")))
}

func (r *Runtime) toolEnvironmentRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	updates := map[string]any{"save_snapshot": false}
	if publicSelector(req, "view") == "compare" {
		if snapshotID, ok := mcpresult.Arguments(req)["snapshot_id"]; ok {
			updates["compare_to"] = snapshotID
		}
	}
	return r.toolEnvironmentInspect(ctx, forwardedRequest(req, updates))
}

func (r *Runtime) toolEnvironmentSnapshotCreate(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolEnvironmentInspect(ctx, forwardedRequest(req, map[string]any{"save_snapshot": true}))
}

func (r *Runtime) toolExtensionDiscover(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolExtensionManage(ctx, publicDispatch(req, "action", publicSelector(req, "view")))
}

func (r *Runtime) toolSkillCall(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolExtensionManage(ctx, forwardedRequest(req, map[string]any{"action": "call", "kind": "skill"}))
}

func (r *Runtime) toolMCPCallPublic(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return r.toolExtensionManage(ctx, forwardedRequest(req, map[string]any{"action": "call", "kind": "mcp"}))
}

func (r *Runtime) toolArtifactReadPublic(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := publicSelector(req, "view")
	if action == "content" {
		action = "read"
	}
	return r.toolArtifactManage(ctx, publicDispatch(req, "action", action))
}

func (r *Runtime) toolObserveChanges(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	workspaceName := session.WorkspaceName
	query := observation.HistoryQuery{
		Workspace: workspaceName,
		SessionID: session.ID,
		Kinds:     []string{"file_change"},
		Limit:     intPayload(envReq.Payload, "limit"),
		Cursor:    stringPayload(envReq.Payload, "cursor"),
	}
	if r.observation == nil || r.observation.store == nil {
		return r.terminalError(envReq, session.ID, workspaceName, "observe_changes_unavailable", "observation store is unavailable")
	}
	events, nextCursor, err := r.observation.store.Query(ctx, query)
	if err != nil {
		return r.terminalError(envReq, "", workspaceName, "observe_changes_error", err.Error())
	}
	changes := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if event.Type != observation.TypeFileChanged || event.Tool != "edit" {
			continue
		}
		payload := map[string]any{}
		_ = json.Unmarshal(event.Output, &payload)
		diffSummary, _ := payload["diff_summary"].(string)
		diffPreview := boundedDiffPreview(diffSummary, cleanDiffTotalPreviewMaxBytes)
		change := map[string]any{
			"id":             event.EventID,
			"event_id":       event.EventID,
			"sequence":       event.Sequence,
			"tool":           event.Tool,
			"status":         event.Status,
			"summary":        event.Summary,
			"diff_summary":   diffPreview.Text,
			"diff_bytes":     len(diffSummary),
			"diff_truncated": diffPreview.Truncated,
			"path":           event.Path,
			"timestamp":      event.CreatedAt,
		}
		if editID, ok := payload["edit_id"].(string); ok && strings.TrimSpace(editID) != "" {
			change["edit_id"] = editID
		}
		if total, ok := payload["total_changed_lines"]; ok {
			change["total_changed_lines"] = total
		}
		if results, ok := payload["results"]; ok {
			change["results"] = results
			if items, ok := results.([]any); ok && len(items) == 1 {
				if item, ok := items[0].(map[string]any); ok {
					if operation, ok := item["operation"].(string); ok && operation != "" {
						change["operation"] = operation
					}
				}
			}
		}
		changes = append(changes, change)
	}
	return r.remoteResult(envReq, session.ID, workspaceName, map[string]any{
		"changes":     changes,
		"next_cursor": nextCursor,
		"count":       len(changes),
	})
}
