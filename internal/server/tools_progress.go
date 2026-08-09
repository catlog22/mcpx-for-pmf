package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
)

// toolProgress records a model-authored, user-visible progress state.
// It does not mutate workspace files; the instrumented lifecycle persists
// semantic milestones and terminal states for observers and reconnects.
func (r *Runtime) toolProgress(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	current, _ := envReq.Payload["current"].(string)
	current = strings.TrimSpace(current)
	if current == "" {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", "progress current is required")
	}
	if len(current) > envelope.MaxIntentBytes {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", fmt.Sprintf("progress current exceeds %d bytes", envelope.MaxIntentBytes))
	}
	result, _ := envReq.Payload["result"].(string)
	result = strings.TrimSpace(result)
	if len(result) > envelope.MaxResultSummaryBytes {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", fmt.Sprintf("progress result exceeds %d bytes", envelope.MaxResultSummaryBytes))
	}
	next, _ := envReq.Payload["next"].(string)
	next = strings.TrimSpace(next)
	status, _ := envReq.Payload["status"].(string)
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "in_progress"
	}
	if !containsString([]string{"in_progress", "completed", "waiting_for_user", "blocked", "failed"}, status) {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", fmt.Sprintf("unsupported progress status %q", status))
	}
	relatedTool, _ := envReq.Payload["related_tool"].(string)
	relatedTool = strings.TrimSpace(relatedTool)
	phase, _ := envReq.Payload["phase"].(string)
	phase = strings.TrimSpace(phase)

	display := current
	if result != "" {
		display += " · result: " + result
	}
	if next != "" {
		display += " · next: " + next
	}
	data := map[string]any{
		"phase":             phase,
		"current":           current,
		"result":            result,
		"status":            status,
		"next":              next,
		"related_tool":      relatedTool,
		"remote_session_id": session.ID,
		"workspace":         session.WorkspaceName,
	}
	return compactToolResult(data, display), nil
}
