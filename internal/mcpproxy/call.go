package mcpproxy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/config"
	"mcpx/internal/logging"
	buildversion "mcpx/internal/version"
)

const (
	mcpConnectTimeout = 30 * time.Second
	mcpListTimeout    = 30 * time.Second
	mcpCallTimeout    = 2 * time.Minute
)

// CallTool starts a stdio MCP client, calls tool, and closes the session.
func CallTool(ctx context.Context, srv config.MCPServer, toolName string, arguments map[string]any) (any, error) {
	session, cancel, err := connect(ctx, srv, mcpConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()

	if arguments == nil {
		arguments = map[string]any{}
	}
	callCtx, callCancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer callCancel()
	res, err := session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	})
	if err != nil {
		return nil, err
	}
	logging.Debug("mcp call ok", "tool", toolName, "cmd", DescribeCommand(srv))
	return res, nil
}

// ListTools starts an upstream stdio server and returns its tools/list items.
func ListTools(ctx context.Context, srv config.MCPServer) ([]*mcp.Tool, error) {
	session, cancel, err := connect(ctx, srv, mcpConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()

	listCtx, listCancel := context.WithTimeout(ctx, mcpListTimeout)
	defer listCancel()
	listed, err := session.ListTools(listCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if listed == nil {
		return nil, nil
	}
	return listed.Tools, nil
}

func connect(ctx context.Context, srv config.MCPServer, timeout time.Duration) (*mcp.ClientSession, context.CancelFunc, error) {
	if srv.Command == "" {
		return nil, func() {}, fmt.Errorf("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	cmd.Env = append(os.Environ(), ExpandEnv(srv.Env)...)
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpx", Version: buildversion.Current}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("connect upstream mcp: %w", err)
	}
	return session, cancel, nil
}
