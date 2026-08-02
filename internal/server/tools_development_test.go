package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/remotesession"
)

func TestProjectTaskAndArtifactRemoteSessionFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.mod":       "module example.invalid/project\n\ngo 1.24\n",
		"main.go":      "package project\n\nfunc Value() int { return 1 }\n",
		"main_test.go": "package project\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal() } }\n",
		"report.txt":   "all checks passed\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "development-token"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspace}}
	cfg.Logging.Enabled = false
	cfg.Security.Commands.Allow = append(cfg.Security.Commands.Allow, `^go test\b`)
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx := auth.ContextWithAuthorization(context.Background(), "Bearer development-token")
	created := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"workspace": "project", "label": "development flow"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	if remoteSessionID == "" {
		t.Fatalf("create=%+v", created)
	}

	listed := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"remote_session_id": remoteSessionID, "include_project_tasks": true})
	if listed["status"] != "ok" {
		t.Fatalf("project task discovery=%+v", listed)
	}
	started := callEnvelope(t, runtime.toolCommandExecute, ctx, map[string]any{"remote_session_id": remoteSessionID, "task": "test", "purpose": "run the project test task", "scope": "workspace", "yield_time_ms": 1})
	data, _ := started["data"].(map[string]any)
	taskID, _ := data["task_id"].(string)
	if started["status"] != "ok" || taskID == "" {
		t.Fatalf("task start=%+v", started)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := callEnvelope(t, runtime.toolTaskManage, ctx, map[string]any{"remote_session_id": remoteSessionID, "action": "status", "task_id": taskID})
		statusData, _ := status["data"].(map[string]any)
		if statusData["status"] != "running" {
			if statusData["exit_code"] != float64(0) {
				t.Fatalf("task status=%+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("project task did not finish")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var logsRequest mcp.CallToolRequest
	logsRequest.Params.Arguments = map[string]any{"intent": "read task logs", "remote_session_id": remoteSessionID, "action": "logs", "task_id": taskID, "stdout_offset": 0, "stderr_offset": 0}
	logsResult, err := runtime.toolTaskManage(ctx, logsRequest)
	if err != nil || len(logsResult.Content) < 1 {
		t.Fatalf("terminal logs result=%+v err=%v", logsResult, err)
	}
	logText, ok := logsResult.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("terminal logs text type=%T", logsResult.Content[0])
	}
	if !strings.Contains(logText.Text, "example.invalid/project") {
		t.Fatalf("task logs must stay inline in the result text: %q", logText.Text)
	}
	logResources, err := runtime.resourceTaskLogs(ctx, mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: "mcpx://remote-sessions/" + remoteSessionID + "/tasks/" + taskID + "/logs"}})
	if err != nil || len(logResources) != 1 {
		t.Fatalf("read terminal log resource: resources=%+v err=%v", logResources, err)
	}

	var registerRequest mcp.CallToolRequest
	registerRequest.Params.Arguments = map[string]any{
		"intent":            "register the test report artifact",
		"remote_session_id": remoteSessionID, "path": "report.txt", "kind": "test_report", "name": "Go test report",
	}
	registeredResult, err := runtime.toolArtifactRegister(ctx, registerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(registeredResult.Content) < 2 {
		t.Fatalf("artifact result content=%+v", registeredResult.Content)
	}
	link, ok := registeredResult.Content[1].(mcp.ResourceLink)
	if !ok || link.URI == "" {
		t.Fatalf("resource link=%+v", registeredResult.Content[1])
	}
	resources, err := runtime.resourceArtifact(ctx, mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: link.URI}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := resources[0].(mcp.TextResourceContents)
	if !ok || text.Text != "all checks passed\n" {
		t.Fatalf("resource=%+v", resources)
	}
}

func TestCommandExecuteInlinesSmallOutputWithoutLogLink(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{
		"intent":            "run a small command",
		"remote_session_id": created.Session.ID,
		"command":           "printf hello-stdout",
		"purpose":           "verify inline stdout rendering",
		"scope":             "workspace",
	}
	result, err := rt.toolCommandExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("small completed command must not attach a log resource: %+v", result.Content)
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	if !strings.Contains(content.Text, "hello-stdout") {
		t.Fatalf("stdout must be inline in the result text: %q", content.Text)
	}
	if strings.Contains(content.Text, "truncated") {
		t.Fatalf("small output must not be flagged truncated: %q", content.Text)
	}
}

func TestCommandExecuteTruncatedOutputStaysInline(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Limits.MaxResultBytes = 64
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{
		"intent":            "run a command with bounded output",
		"remote_session_id": created.Session.ID,
		"command":           "printf abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz",
		"purpose":           "verify truncated output stays inline",
		"scope":             "workspace",
	}
	result, err := rt.toolCommandExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("no file resource may be attached to the conversation: %+v", result.Content)
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	if !strings.Contains(content.Text, "Output truncated") {
		t.Fatalf("truncation must be stated in the text: %q", content.Text)
	}
	if !strings.Contains(content.Text, "task_manage") {
		t.Fatalf("truncation notice must point to task_manage: %q", content.Text)
	}
}
