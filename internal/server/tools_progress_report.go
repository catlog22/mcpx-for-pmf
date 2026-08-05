package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/envelope"
)

// toolProgressReport records a model-authored, user-visible progress update
// when the agent will not immediately call another workspace tool. This is
// intentionally a no-op for the workspace: the instrumented tool lifecycle
// persists the summary and the observer renders it like any other call.
func (r *Runtime) toolProgressReport(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	summary, _ := envReq.Payload["summary"].(string)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", "progress summary is required")
	}
	if len(summary) > envelope.MaxIntentBytes {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", fmt.Sprintf("progress summary exceeds %d bytes", envelope.MaxIntentBytes))
	}
	resultSummary, _ := envReq.Payload["result_summary"].(string)
	resultSummary = strings.TrimSpace(resultSummary)
	if len(resultSummary) > envelope.MaxResultSummaryBytes {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", fmt.Sprintf("result summary exceeds %d bytes", envelope.MaxResultSummaryBytes))
	}
	nextStep, _ := envReq.Payload["next_step"].(string)
	nextStep = strings.TrimSpace(nextStep)
	status, _ := envReq.Payload["status"].(string)
	status = strings.TrimSpace(status)
	if status == "" {
		status = "in_progress"
	}
	if !containsString([]string{"in_progress", "completed", "waiting_for_user", "blocked"}, status) {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", fmt.Sprintf("unsupported progress status %q", status))
	}
	relatedTool, _ := envReq.Payload["related_tool"].(string)
	relatedTool = strings.TrimSpace(relatedTool)
	phase, _ := envReq.Payload["phase"].(string)
	phase = strings.TrimSpace(phase)

	display := summary
	if resultSummary != "" {
		display += " · result: " + resultSummary
	}
	if nextStep != "" {
		display += " · next: " + nextStep
	}
	data := map[string]any{
		"phase":             phase,
		"summary":           display,
		"progress_summary":  summary,
		"result_summary":    resultSummary,
		"status":            status,
		"next_step":         nextStep,
		"related_tool":      relatedTool,
		"remote_session_id": session.ID,
		"workspace":         session.WorkspaceName,
	}
	return compactToolResult(data, display), nil
}
