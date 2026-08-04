package server

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"mcpx/internal/remotesession"
)

func TestCommandExecuteBindsPurposeAndWorkspaceScope(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands.Allow = append(rt.cfg.Security.Commands.Allow, `^printf\b`)
	rt.cfg.Security.Commands.Confirm = append(rt.cfg.Security.Commands.Confirm, `^echo\b`)
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

	missingPurpose := mcp.CallToolRequest{}
	missingPurpose.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID, "command": "printf context",
	}
	missingResult, err := rt.toolCommandExecute(context.Background(), missingPurpose)
	if err != nil {
		t.Fatal(err)
	}
	missing := decodeToolResult(t, missingResult)
	if missing["status"] != "failed" {
		t.Fatalf("missing purpose was accepted: %+v", missing)
	}

	invalidScope := mcp.CallToolRequest{}
	invalidScope.Params.Arguments = map[string]any{
		"intent":            "validate command scope",
		"remote_session_id": created.Session.ID, "command": "printf context",
		"purpose": "verify context binding", "scope": "host",
	}
	invalidResult, err := rt.toolCommandExecute(context.Background(), invalidScope)
	if err != nil {
		t.Fatal(err)
	}
	invalid := decodeToolResult(t, invalidResult)
	if invalid["status"] != "failed" {
		t.Fatalf("invalid scope was accepted: %+v", invalid)
	}

	// Confirm rules create a pending action carrying the exact command context.
	confirmationRequest := mcp.CallToolRequest{}
	confirmationRequest.Params.Arguments = map[string]any{
		"intent":            "request semantic confirmation for a command",
		"remote_session_id": created.Session.ID, "command": "echo confirmation",
		"purpose": "verify confirmation context", "scope": "workspace",
	}
	confirmationResult, err := rt.toolCommandExecute(context.Background(), confirmationRequest)
	if err != nil {
		t.Fatal(err)
	}
	confirmationResponse := decodeToolResult(t, confirmationResult)
	confirmationData, _ := confirmationResponse["data"].(map[string]any)
	if confirmationResponse["status"] != "waiting_confirmation" || confirmationData["purpose"] != "verify confirmation context" || confirmationData["scope"] != "workspace" {
		t.Fatalf("confirm rule must create semantic confirmation: %+v", confirmationResponse)
	}
	if digest, _ := confirmationData["command_digest"].(string); !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("missing command digest: %+v", confirmationData)
	}
	confirmRequest := mcp.CallToolRequest{}
	confirmRequest.Params.Arguments = map[string]any{
		"intent":            "用户已确认执行该命令",
		"remote_session_id": created.Session.ID, "command": "echo confirmation",
		"purpose": "verify confirmation context", "scope": "workspace", "confirmation_token": confirmationData["confirmation_token"],
	}
	confirmed, err := rt.toolCommandExecute(context.Background(), confirmRequest)
	if err != nil {
		t.Fatal(err)
	}
	confirmedResponse := decodeToolResult(t, confirmed)
	if confirmedResponse["status"] != "ok" {
		t.Fatalf("confirmed command failed: %+v", confirmedResponse)
	}

	valid := mcp.CallToolRequest{}
	valid.Params.Arguments = map[string]any{
		"intent":            "execute the scoped command",
		"remote_session_id": created.Session.ID, "command": "printf context",
		"purpose": "verify context binding", "scope": "workspace",
	}
	validResult, err := rt.toolCommandExecute(context.Background(), valid)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, validResult)
	data, _ := response["data"].(map[string]any)
	if response["status"] != "ok" || data["purpose"] != "verify context binding" || data["scope"] != "workspace" || data["workspace_scoped"] != true {
		t.Fatalf("execution context was not returned: %+v", response)
	}
	if digest, _ := data["command_digest"].(string); !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("missing command digest: %+v", data)
	}
}
