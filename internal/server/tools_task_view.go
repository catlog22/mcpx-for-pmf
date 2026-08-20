package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/tasks"
)

// toolTaskView exposes the delegated-task registry written by pi_window send:
// action=view returns one task (delegated_task_id) or every task recorded for
// the session. Settled {taskID}.result.json companion files are merged into
// the registry record so callers see final status/result/summary.
func (r *Runtime) toolTaskView(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	if action != "view" {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "INVALID_ACTION", fmt.Sprintf("task_result_view does not support action %q", action))
	}
	if r.delegated == nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "registry_unavailable", "delegated task registry is not initialized")
	}
	taskID := strings.TrimSpace(stringPayload(envReq.Payload, "delegated_task_id"))
	if taskID != "" {
		task, err := r.delegated.Get(session.ID, taskID)
		if err != nil {
			if errors.Is(err, tasks.ErrNotFound) {
				return r.terminalError(envReq, session.ID, session.WorkspaceName, "task_not_found",
					fmt.Sprintf("delegated task %q not found for session %s", taskID, session.ID))
			}
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "task_registry_error", err.Error())
		}
		merged, err := r.mergeDelegatedResult(&task)
		if err != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "task_result_invalid", err.Error())
		}
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "task_result_view", Status: "ok", Detail: map[string]any{"delegated_task_id": taskID}})
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, delegatedTaskData(task, merged))
	}
	list, err := r.delegated.ListBySession(session.ID)
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "task_registry_error", err.Error())
	}
	items := make([]map[string]any, 0, len(list))
	for i := range list {
		merged, mergeErr := r.mergeDelegatedResult(&list[i])
		if mergeErr != nil {
			return r.terminalError(envReq, session.ID, session.WorkspaceName, "task_result_invalid", mergeErr.Error())
		}
		items = append(items, delegatedTaskData(list[i], merged))
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "task_result_view", Status: "ok", Detail: map[string]any{"count": len(items)}})
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{
		"remote_session_id": session.ID,
		"count":             len(items),
		"tasks":             items,
	})
}

// mergeDelegatedResult folds the {taskID}.result.json companion file into a
// registry record when present. A result file means the delegated work
// settled: its status wins, defaulting to completed; result, summary,
// completion time and error override the delivery-time record.
func (r *Runtime) mergeDelegatedResult(task *tasks.DelegatedTask) (bool, error) {
	result, err := r.delegated.ReadResult(task.RemoteSessionID, task.TaskID)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if status := strings.TrimSpace(result.Status); status != "" {
		task.Status = status
	} else {
		task.Status = tasks.StatusCompleted
	}
	if result.Result != "" {
		task.Result = result.Result
	}
	if len(result.ResultSummary) > 0 {
		task.ResultSummary = result.ResultSummary
	}
	if result.CompletedAt != nil {
		task.CompletedAt = result.CompletedAt
	}
	if result.Error != "" {
		task.Error = result.Error
	}
	return true, nil
}

// delegatedTaskData renders one task with stable snake_case wire keys plus a
// result_merged marker distinguishing registry state from settled results.
func delegatedTaskData(task tasks.DelegatedTask, resultMerged bool) map[string]any {
	encoded, err := json.Marshal(task)
	if err != nil {
		return map[string]any{"task_id": task.TaskID, "status": task.Status, "result_merged": resultMerged}
	}
	var data map[string]any
	if json.Unmarshal(encoded, &data) != nil || data == nil {
		data = map[string]any{}
	}
	data["result_merged"] = resultMerged
	return data
}
