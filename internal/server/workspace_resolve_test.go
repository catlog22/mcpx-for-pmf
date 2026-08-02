package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
)

func newWorkspaceRuntime(t *testing.T, names ...string) *Runtime {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	entries := make([]config.WorkspaceEntry, 0, len(names))
	for _, name := range names {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, config.WorkspaceEntry{Name: name, Path: path})
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	cfg.Workspaces = entries
	cfg.Logging.Enabled = false
	cfg.Logging.Dir = filepath.Join(home, "logs")
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestResolveExplicitWorkspaceByName(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ws, remoteID, err := rt.resolveExplicitWorkspace(context.Background(), principal, envelope.Request{
		Workspace: "demo", Payload: map[string]any{},
	})
	if err != nil || remoteID != "" || ws.Name != "demo" {
		t.Fatalf("workspace=%+v remote=%q err=%v", ws, remoteID, err)
	}
}

func TestResolveRemoteSessionWorkspaceWithoutTransportBinding(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, _ := rt.reg.Get("demo")
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path, Label: "explicit session",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := envelope.Request{RemoteSessionID: created.Session.ID, Payload: map[string]any{}}
	for i := 0; i < 2; i++ {
		ws, remoteID, err := rt.resolveExplicitWorkspace(context.Background(), principal, request)
		if err != nil || remoteID != created.Session.ID || ws.Path != registered.Path {
			t.Fatalf("iteration=%d workspace=%+v remote=%q err=%v", i, ws, remoteID, err)
		}
	}
}

func TestWorkspaceInfoDoesNotAutoSelectSingleWorkspace(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"intent": "inspect the project workspace", "action": "project"}
	out, err := rt.toolRuntimeInspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := out.Content[0].(mcp.TextContent).Text
	var env map[string]any
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatal(err)
	}
	errObj, _ := env["error"].(map[string]any)
	if env["status"] != "error" || errObj["code"] != "REMOTE_SESSION_REQUIRED" {
		t.Fatalf("expected explicit workspace error, got %s", text)
	}
}

func TestRegisteredToolsExcludeCompatibilityInterfaces(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	rt.registerTools(protocol)
	tools := protocol.ListTools()
	for _, removed := range []string{"workspace_select", "code_read", "file_patch"} {
		if _, exists := tools[removed]; exists {
			t.Fatalf("removed compatibility tool %q is still registered", removed)
		}
	}
	for _, required := range []string{"workspace_list", "session_open", "file_read", "change_execute", "change_manage"} {
		if _, exists := tools[required]; !exists {
			t.Fatalf("required tool %q is missing", required)
		}
	}
}
