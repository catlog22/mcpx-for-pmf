package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
	"mcpx/internal/observation"
)

// toolObserve is the clean-core observation entry point. P0 gives changes a
// dedicated inline-diff path while reusing the existing bounded history and
// task readers for the remaining views.
func (r *Runtime) toolObserve(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch publicSelector(req, "view") {
	case "status":
		if stringPayload(mcpresult.Arguments(req), "task_id") != "" {
			return r.toolObserveTask(ctx, req, "status")
		}
		return r.toolObserveStatus(ctx, req)
	case "history":
		return r.toolWorkspaceHistoryRead(ctx, req)
	case "changes":
		return r.toolObserveChanges(ctx, req)
	case "logs":
		return r.toolObserveTask(ctx, req, "logs")
	case "diff":
		if stringPayload(mcpresult.Arguments(req), "edit_id") != "" {
			return r.toolObserveEditDiff(ctx, req)
		}
		return r.toolWorkspaceChanges(ctx, req)
	default:
		return r.invalidAction(ctx, req, "observe", publicSelector(req, "view"))
	}
}

func (r *Runtime) toolObserveStatus(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	data := map[string]any{
		"remote_session_id": session.ID,
		"workspace":         session.WorkspaceName,
		"session_status":    session.Status,
	}
	if r.observation != nil && r.observation.store != nil {
		events, _, err := r.observation.store.Query(ctx, observation.HistoryQuery{
			Workspace: session.WorkspaceName, SessionID: session.ID, Limit: 1,
		})
		if err != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "observe_status_error", err.Error())
		}
		if len(events) > 0 {
			data["latest_event"] = historyEventView(events[0])
		}
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
}
