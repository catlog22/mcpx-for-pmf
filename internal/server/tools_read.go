package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolRead is the clean-core read entry point. File/search/list/context keep
// the existing bounded source implementations; environment is normalized to
// the existing current-environment inspection path.
func (r *Runtime) toolRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch publicSelector(req, "view") {
	case "file", "search", "list", "context":
		return r.toolSourceRead(ctx, req)
	case "environment":
		return r.toolEnvironmentRead(ctx, publicDispatch(req, "view", "current"))
	default:
		return r.invalidAction(ctx, req, "read", publicSelector(req, "view"))
	}
}
