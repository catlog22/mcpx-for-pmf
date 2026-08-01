package mcpproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/config"
	"mcpx/internal/logging"
	buildversion "mcpx/internal/version"
)

// CallTool starts a stdio MCP client, initializes, calls tool, closes.
func CallTool(ctx context.Context, srv config.MCPServer, toolName string, arguments map[string]any) (any, error) {
	if srv.Command == "" {
		return nil, fmt.Errorf("empty command")
	}
	env := ExpandEnv(srv.Env)
	c, err := client.NewStdioMCPClient(srv.Command, env, srv.Args...)
	if err != nil {
		return nil, fmt.Errorf("stdio client: %w", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "mcpx", Version: buildversion.Current}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	call := mcp.CallToolRequest{}
	call.Params.Name = toolName
	if arguments == nil {
		arguments = map[string]any{}
	}
	call.Params.Arguments = arguments
	res, err := c.CallTool(ctx, call)
	if err != nil {
		return nil, err
	}
	logging.Debug("mcp call ok", "tool", toolName, "cmd", DescribeCommand(srv))
	return res, nil
}

// ListTools starts an upstream stdio server, initializes it, and returns its
// complete paginated tools/list response. Callers opt into this because some
// upstream commands are expensive or interactive to start.
func ListTools(ctx context.Context, srv config.MCPServer) ([]mcp.Tool, error) {
	if srv.Command == "" {
		return nil, fmt.Errorf("empty command")
	}
	c, err := client.NewStdioMCPClient(srv.Command, ExpandEnv(srv.Env), srv.Args...)
	if err != nil {
		return nil, fmt.Errorf("stdio client: %w", err)
	}
	defer func() { _ = c.Close() }()
	discoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "mcpx", Version: buildversion.Current}
	if _, err := c.Initialize(discoveryCtx, initReq); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	listed, err := c.ListTools(discoveryCtx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	return listed.Tools, nil
}
