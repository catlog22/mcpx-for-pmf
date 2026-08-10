package mcpproxy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/config"
	"mcpx/internal/logging"
	buildversion "mcpx/internal/version"
)

const (
	mcpConnectTimeout      = 30 * time.Second
	mcpListTimeout         = 30 * time.Second
	mcpCallTimeout         = 2 * time.Minute
	clientStartedAtMetaKey = "mcpx/started_at_ms"
)

var (
	clientProgressHeartbeatInterval = 20 * time.Second
	nextProgressToken               atomic.Uint64
)

// ToolProgress is a client-side progress update for one upstream tools/call.
// Synthetic updates are emitted by the MCPX client watchdog when the upstream
// server has not sent a progress notification within the heartbeat interval.
type ToolProgress struct {
	Message   string
	Progress  float64
	Total     float64
	Elapsed   time.Duration
	Synthetic bool
}

// ProgressHandler receives progress for an upstream tools/call.
type ProgressHandler func(ToolProgress)

// CallTool starts a stdio MCP client, calls tool, and closes the session.
func CallTool(ctx context.Context, srv config.MCPServer, toolName string, arguments map[string]any) (any, error) {
	return CallToolWithProgress(ctx, srv, toolName, arguments, nil)
}

// CallToolWithProgress calls an upstream tool while requesting native MCP
// progress notifications. When onProgress is non-nil, a client-side watchdog
// also emits a synthetic update if no upstream progress arrives in time.
func CallToolWithProgress(ctx context.Context, srv config.MCPServer, toolName string, arguments map[string]any, onProgress ProgressHandler) (any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}

	progressToken := ""
	progressReset := make(chan struct{}, 1)
	callStarted := time.Now()
	var callbackMu sync.Mutex
	deliver := func(update ToolProgress) {
		if onProgress == nil {
			return
		}
		callbackMu.Lock()
		defer callbackMu.Unlock()
		onProgress(update)
	}
	resetWatchdog := func() {
		select {
		case progressReset <- struct{}{}:
		default:
		}
	}

	var clientOptions *mcp.ClientOptions
	if onProgress != nil {
		progressToken = fmt.Sprintf("mcpx-progress-%d", nextProgressToken.Add(1))
		clientOptions = &mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
				if req == nil || req.Params == nil || fmt.Sprint(req.Params.ProgressToken) != progressToken {
					return
				}
				deliver(ToolProgress{
					Message:  req.Params.Message,
					Progress: req.Params.Progress,
					Total:    req.Params.Total,
					Elapsed:  time.Since(callStarted),
				})
				resetWatchdog()
			},
		}
	}

	session, cancel, err := connect(ctx, srv, mcpConnectTimeout, clientOptions)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()

	callCtx, callCancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer callCancel()
	callStarted = time.Now()
	params := newCallToolParams(toolName, arguments, progressToken, callStarted)
	stopHeartbeat := startClientProgressHeartbeat(callCtx, toolName, callStarted, progressReset, deliver)
	defer stopHeartbeat()

	res, err := session.CallTool(callCtx, params)
	if err != nil {
		return nil, err
	}
	logging.Debug("mcp call ok", "tool", toolName, "cmd", DescribeCommand(srv))
	return res, nil
}

func newCallToolParams(toolName string, arguments map[string]any, progressToken string, started time.Time) *mcp.CallToolParams {
	params := &mcp.CallToolParams{
		Meta:      mcp.Meta{clientStartedAtMetaKey: started.UnixMilli()},
		Name:      toolName,
		Arguments: arguments,
	}
	if progressToken != "" {
		params.SetProgressToken(progressToken)
	}
	return params
}

func startClientProgressHeartbeat(ctx context.Context, toolName string, started time.Time, reset <-chan struct{}, deliver ProgressHandler) func() {
	if deliver == nil || clientProgressHeartbeatInterval <= 0 {
		return func() {}
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(clientProgressHeartbeatInterval)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				deliver(ToolProgress{
					Message:   fmt.Sprintf("%s is still running", toolName),
					Progress:  time.Since(started).Seconds(),
					Elapsed:   time.Since(started),
					Synthetic: true,
				})
				timer.Reset(clientProgressHeartbeatInterval)
			case <-reset:
				resetProgressTimer(timer, clientProgressHeartbeatInterval)
			case <-heartbeatCtx.Done():
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func resetProgressTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}

// ListTools starts an upstream stdio server and returns its tools/list items.
func ListTools(ctx context.Context, srv config.MCPServer) ([]*mcp.Tool, error) {
	session, cancel, err := connect(ctx, srv, mcpConnectTimeout, nil)
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

func connect(ctx context.Context, srv config.MCPServer, timeout time.Duration, options *mcp.ClientOptions) (*mcp.ClientSession, context.CancelFunc, error) {
	if srv.Command == "" {
		return nil, func() {}, fmt.Errorf("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	cmd.Env = append(os.Environ(), ExpandEnv(srv.Env)...)
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpx", Version: buildversion.Current}, options)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("connect upstream mcp: %w", err)
	}
	return session, cancel, nil
}
