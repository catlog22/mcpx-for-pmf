package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
	"mcpx/internal/terminal"
)

func TestConfigRoundTripInNew(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	ws := filepath.Join(home, "p")
	_ = os.MkdirAll(ws, 0o755)
	rt, err := New(Options{WorkspaceFlag: ws})
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.reg.List()) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(rt.reg.List()))
	}
}

func TestExecPipelineAllowConfirmDeny(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	ws := filepath.Join(home, "proj")
	_ = os.MkdirAll(ws, 0o755)
	cfg := config.DefaultConfig()
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "proj", Path: ws}}
	cfg.Security.Commands = config.CommandRules{
		Allow:   []string{`^echo\b`},
		Confirm: []string{`^npm install`},
		Deny:    []string{`^rm -rf /`},
	}
	cfg.Logging.Dir = filepath.Join(home, "logs")
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	eff := rt.effectiveConfig(ws)
	if security.MatchCommand(eff.Security.Commands, "echo hi") != security.Allow {
		t.Fatal("allow")
	}
	if security.MatchCommand(eff.Security.Commands, "npm install") != security.Confirm {
		t.Fatal("confirm rules must require approval")
	}
	if security.MatchCommand(eff.Security.Commands, "rm -rf /") != security.Deny {
		t.Fatal("deny")
	}
	res, err := terminal.Exec(context.Background(), terminal.ExecOptions{WorkDir: ws, Command: "echo pipeline-ok"})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "pipeline-ok") {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestTerminalExecRunsAfterApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	ws := filepath.Join(home, "proj")
	_ = os.MkdirAll(ws, 0o755)
	cfg := config.DefaultConfig()
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "proj", Path: ws}}
	// Confirm rules require approval before starting the task.
	cfg.Security.Commands = config.CommandRules{
		Confirm: []string{`^echo\b`},
	}
	cfg.Logging.Dir = filepath.Join(home, "logs")
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "proj", WorkspacePath: ws, Label: "no approval test",
	})
	if err != nil {
		t.Fatal(err)
	}

	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"command":           "echo approved-run",
		"purpose":           "run the command test",
		"scope":             "workspace",
	}
	out, err := rt.toolCommandExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, out)
	if response["status"] != "need_confirmation" {
		t.Fatalf("command must require approval: %+v", response)
	}
	data, _ := response["data"].(map[string]any)
	approvalID, _ := data["approval_id"].(string)
	if approvalID == "" {
		t.Fatalf("missing approval id: %+v", response)
	}
	approvalReq := mcp.CallToolRequest{}
	approvalReq.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID, "approval_id": approvalID, "approve": true,
	}
	approved, err := rt.toolApprovalConfirm(context.Background(), approvalReq)
	if err != nil {
		t.Fatal(err)
	}
	response = decodeToolResult(t, approved)
	if response["status"] != "ok" {
		t.Fatalf("approved command did not run: %+v", response)
	}
	data, _ = response["data"].(map[string]any)
	stdout, _ := data["stdout"].(string)
	if !strings.Contains(stdout, "approved-run") {
		t.Fatalf("stdout %v", data)
	}
	if pending := rt.approvals.ListRemoteSession(created.Session.ID); len(pending) != 0 {
		t.Fatalf("approval must be consumed after execution: %+v", pending)
	}
}

func TestTerminalStartUsesCommandPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	ws := filepath.Join(home, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "proj", Path: ws}}
	cfg.Security.Commands = config.CommandRules{Default: "deny"}
	cfg.Logging.Dir = filepath.Join(home, "logs")
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "proj", WorkspacePath: ws, Label: "terminal policy test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"command":           "echo must-not-start",
		"purpose":           "verify denied command policy",
		"scope":             "workspace",
	}
	out, err := rt.toolCommandExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var response envelope.Response
	text := out.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != envelope.StatusDenied {
		t.Fatalf("got %s: %s", response.Status, text)
	}
}

func TestRequireAuthBearerFromContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	t.Setenv("MCPX_TEST_BEARER", "") // ensure HTTP path, not env fallback
	ws := filepath.Join(home, "proj")
	_ = os.MkdirAll(ws, 0o755)
	cfg := config.DefaultConfig()
	cfg.Auth.Token = "secret-token"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "proj", Path: ws}}
	cfg.Logging.Dir = filepath.Join(home, "logs")
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	// missing header
	if _, err := rt.principalFromContext(context.Background()); err == nil {
		t.Fatal("expected missing credentials to be rejected")
	}

	// wrong token
	wrong := auth.ContextWithAuthorization(context.Background(), "Bearer wrong")
	if _, err := rt.principalFromContext(wrong); err == nil {
		t.Fatal("expected wrong credentials to be rejected")
	}

	// ok via context injection path used by HTTP
	ctx := auth.ContextWithAuthorization(context.Background(), "Bearer secret-token")
	principal, err := rt.principalFromContext(ctx)
	if err != nil || principal.Kind != "bearer" {
		t.Fatalf("expected bearer principal, got %+v err=%v", principal, err)
	}
}
