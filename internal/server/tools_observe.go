package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
	"mcpx/internal/observation"
)

// toolObserve is the clean-core observation entry point. Plan Task IDs and
// execution Task IDs are distinct public concepts and are never inferred from
// an ambiguous task_id field.
func (r *Runtime) toolObserve(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	view := publicSelector(req, "view")
	args := mcpresult.Arguments(req)
	planTaskID := stringPayload(args, "plan_task_id")
	executionTaskID := stringPayload(args, "execution_task_id")
	if planTaskID != "" && executionTaskID != "" && view != "history" {
		return r.observeTaskIDError(ctx, req, "OBSERVE_TASK_ID_CONFLICT", "plan_task_id and execution_task_id cannot both select a single observe target")
	}
	switch view {
	case "session":
		return r.toolObserveStatus(ctx, req)
	case "status":
		if planTaskID != "" {
			return r.observeTaskIDError(ctx, req, "EXECUTION_TASK_ID_REQUIRED", "view=status requires execution_task_id; use view=plan for plan_task_id")
		}
		return r.toolObserveTask(ctx, req, "status")
	case "plan":
		if executionTaskID != "" {
			return r.observeTaskIDError(ctx, req, "PLAN_TASK_ID_REQUIRED", "view=plan requires plan_task_id; use view=status for execution_task_id")
		}
		return r.toolObservePlan(ctx, req)
	case "history":
		return r.toolWorkspaceHistoryRead(ctx, req)
	case "changes":
		return r.toolObserveChanges(ctx, req)
	case "logs":
		if planTaskID != "" {
			return r.observeTaskIDError(ctx, req, "EXECUTION_TASK_ID_REQUIRED", "view=logs requires execution_task_id; Plan Tasks do not own terminal logs")
		}
		return r.toolObserveTask(ctx, req, "logs")
	case "diff":
		if stringPayload(args, "edit_id") != "" {
			return r.toolObserveEditDiff(ctx, req)
		}
		return r.toolWorkspaceChanges(ctx, req)
	default:
		return r.invalidAction(ctx, req, "observe", view)
	}
}

func (r *Runtime) toolObservePlan(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	planTaskID := stringPayload(envReq.Payload, "plan_task_id")
	if planTaskID == "" {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "PLAN_TASK_ID_REQUIRED", "plan_task_id is required for observe(view=plan)")
	}
	planID, task, err := r.plans.FindTask(ctx, session.ID, planTaskID)
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "PLAN_TASK_NOT_FOUND", "plan_task_id does not belong to this Remote Session")
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, planTaskMap(planID, task))
}

func (r *Runtime) observeTaskIDError(ctx context.Context, req *mcp.CallToolRequest, code, message string) (*mcp.CallToolResult, error) {
	envReq, _, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	return r.terminalError(envReq, envReq.RemoteSessionID, envReq.Workspace, code, message)
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
