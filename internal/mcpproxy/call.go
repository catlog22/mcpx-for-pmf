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

// CallTool starts a stdio MCP client, calls tool, and closes the session.
func CallTool(ctx context.Context, srv config.MCPServer, toolName string, arguments map[string]any) (any, error) {
	session, err := connect(ctx, srv, 60*time.Second)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	if arguments == nil {
		arguments = map[string]any{}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
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
	session, err := connect(ctx, srv, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if listed == nil {
		return nil, nil
	}
	return listed.Tools, nil
}

func connect(ctx context.Context, srv config.MCPServer, timeout time.Duration) (*mcp.ClientSession, error) {
	if srv.Command == "" {
		return nil, fmt.Errorf("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	// cancel is not deferred: session owns the connection; caller closes session.
	_ = cancel

	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	cmd.Env = append(os.Environ(), ExpandEnv(srv.Env)...)
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpx", Version: buildversion.Current}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect upstream mcp: %w", err)
	}
	return session, nil
}
