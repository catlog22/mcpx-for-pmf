package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

// toolArtifactClean is the single artifact entry point. Registration, listing
// and chunked reading share the same Remote Session scope and response shape.
func (r *Runtime) toolArtifactClean(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	switch action := toolAction(req); action {
	case "register":
		return r.withCleanIdempotency(ctx, req, "artifact", mcpresult.Arguments(req), r.toolArtifactRegister)
	case "list":
		return r.toolArtifactList(ctx, req)
	case "read":
		return r.toolArtifactRead(ctx, req)
	default:
		envReq, _, fail := r.remoteRequest(ctx, req)
		if fail != nil {
			return fail, nil
		}
		return r.terminalError(envReq, envReq.RemoteSessionID, envReq.Workspace, "INVALID_ACTION", fmt.Sprintf("artifact does not support action %q", action))
	}
}
