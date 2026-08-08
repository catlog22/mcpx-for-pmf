package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

// toolPlanClean maps the stable plan contract onto the persistent plan
// service. The service keeps its historical transition names internally so
// existing operation tests and stored plan events remain compatible.
func (r *Runtime) toolPlanClean(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	action := toolAction(req)
	internal := action
	switch action {
	case "read":
		internal = "get"
	case "advance":
		internal = "start_task"
	case "complete":
		internal = "complete_task"
	case "block":
		internal = "block_task"
	case "create", "replan", "deliver":
	default:
		envReq, _, fail := r.remoteRequest(ctx, req)
		if fail != nil {
			return fail, nil
		}
		return r.terminalError(envReq, envReq.RemoteSessionID, envReq.Workspace, "INVALID_ACTION", fmt.Sprintf("plan does not support action %q", action))
	}
	forwarded := forwardedRequest(req, map[string]any{"action": internal})
	if action == "read" || action == "deliver" {
		return r.toolPlanManage(ctx, forwarded)
	}
	return r.withCleanIdempotency(ctx, req, "plan", mcpresult.Arguments(req), func(callCtx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return r.toolPlanManage(callCtx, forwarded)
	})
}
