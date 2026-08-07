package server

import (
	"mcpx/internal/mcpresult"

	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/remotesession"
)

func TestCapabilityCatalogMatchesRegisteredTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	registered := make([]string, 0, len(runtime.listedToolMap()))
	for name := range runtime.listedToolMap() {
		registered = append(registered, name)
	}
	sort.Strings(registered)
	declared := capabilityToolNames()
	if len(registered) != len(declared) {
		t.Fatalf("tool count mismatch: registered=%d declared=%d\nregistered=%v\ndeclared=%v", len(registered), len(declared), registered, declared)
	}
	for index := range registered {
		if registered[index] != declared[index] {
			t.Fatalf("tool catalog mismatch at %d: registered=%q declared=%q", index, registered[index], declared[index])
		}
	}
}

func TestMachineToolCapabilitiesApplyRoleAndFeatureState(t *testing.T) {
	effective := config.DefaultConfig()
	effective.Terminal.Enabled = false
	viewer := remotesession.Session{Role: "viewer"}
	items := machineToolCapabilities(effective, &viewer)
	states := map[string]string{}
	for _, item := range items {
		states[item["name"].(string)] = item["state"].(string)
	}
	if states["source_read"] != "available" || states["change"] != "forbidden" || states["command_run"] != "disabled" {
		t.Fatalf("unexpected capability states: %+v", states)
	}
}

func TestCapabilityListIncludesInstructionsSkillsAndRoleState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(workspace, ".skills", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	globalAgents := filepath.Join(home, "GLOBAL_AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("# Global\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".skills", "review", "SKILL.md"), []byte("---\nname: review\ndescription: Review code\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "developer-token"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspace}}
	cfg.Discovery.Instructions.GlobalAgentsPath = globalAgents
	cfg.Discovery.Skills.Dirs = []string{".skills"}
	cfg.Logging.Enabled = false
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx := authContextForCapabilities()
	created := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"workspace": "project"})
	createdData := created["data"].(map[string]any)
	openedSkills := createdData["skills"].(map[string]any)["items"].([]any)
	if len(openedSkills) != 1 || openedSkills[0].(map[string]any)["name"] != "review" {
		t.Fatalf("session_open skills should follow server config: %+v", createdData["skills"])
	}
	remoteID, _ := created["remote_session_id"].(string)
	response := callEnvelope(t, runtime.toolCapabilityList, ctx, map[string]any{"remote_session_id": remoteID})
	data := response["data"].(map[string]any)
	if data["revision"] == "" || data["schema_source"] != "tools/list" {
		t.Fatalf("capability metadata: %+v", data)
	}
	instructions := data["instructions"].(map[string]any)["documents"].([]any)
	if len(instructions) != 2 {
		t.Fatalf("instructions: %+v", instructions)
	}
	skills := data["skills"].(map[string]any)["items"].([]any)
	if len(skills) != 1 || skills[0].(map[string]any)["name"] != "review" {
		t.Fatalf("skills: %+v", skills)
	}
	tools := data["tools"].([]any)
	foundChangeApply := false
	for _, raw := range tools {
		item := raw.(map[string]any)
		if item["name"] == "change" {
			foundChangeApply = item["state"] == "available"
		}
	}
	if !foundChangeApply {
		t.Fatal("owner capability did not expose change as available")
	}

	readRequest := mcpresult.Request(map[string]any{"intent": "read project instructions", "remote_session_id": remoteID, "id": "project"})

	readResult, err := runtime.toolAgentInstructionRead(ctx, readRequest)
	if err != nil {
		t.Fatal(err)
	}
	readResponse := decodeToolResult(t, readResult)
	if readResponse["data"].(map[string]any)["content"] != "# Project\n" {
		t.Fatalf("instruction read: %+v", readResponse)
	}
}

func authContextForCapabilities() context.Context {
	return auth.ContextWithAuthorization(context.Background(), "Bearer developer-token")
}
