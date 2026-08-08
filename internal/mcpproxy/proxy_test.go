package mcpproxy

import (
	"testing"

	"mcpx/internal/config"
)

func TestManagersKeepWorkspaceConfigsIsolated(t *testing.T) {
	workspaceA := NewManager(true, config.MCPFile{MCPServers: map[string]config.MCPServer{
		"db": {Command: "db-a", Env: map[string]string{"TOKEN": "a"}},
	}})
	workspaceB := NewManager(true, config.MCPFile{MCPServers: map[string]config.MCPServer{
		"db": {Command: "db-b", Env: map[string]string{"TOKEN": "b"}},
	}})

	a, ok := workspaceA.ServerConfig("db")
	if !ok || a.Command != "db-a" || a.Env["TOKEN"] != "a" {
		t.Fatalf("workspace A config changed: %+v ok=%v", a, ok)
	}
	b, ok := workspaceB.ServerConfig("db")
	if !ok || b.Command != "db-b" || b.Env["TOKEN"] != "b" {
		t.Fatalf("workspace B config changed: %+v ok=%v", b, ok)
	}
}

func TestDisabledManagerExposesNoServers(t *testing.T) {
	m := NewManager(false, config.MCPFile{MCPServers: map[string]config.MCPServer{
		"db": {Command: "db"},
	}})
	if got := m.List(); len(got) != 0 {
		t.Fatalf("disabled list: %+v", got)
	}
	if _, ok := m.ServerConfig("db"); ok {
		t.Fatal("disabled manager returned server config")
	}
}

func TestListReturnsStableMachineReadableDescriptorsWithoutSecrets(t *testing.T) {
	m := NewManager(true, config.MCPFile{MCPServers: map[string]config.MCPServer{
		"zeta":  {Command: "zeta", Env: map[string]string{"TOKEN": "secret"}},
		"alpha": {Command: "alpha"},
	}})
	items := m.List()
	if len(items) != 2 || items[0]["name"] != "alpha" || items[1]["name"] != "zeta" {
		t.Fatalf("unstable descriptors: %+v", items)
	}
	if _, exposed := items[1]["env"]; exposed {
		t.Fatalf("descriptor exposed environment: %+v", items[1])
	}
	discovery, ok := items[0]["tool_discovery"].(map[string]any)
	if !ok || discovery["tool"] != "discover" {
		t.Fatalf("missing discovery invocation: %+v", items[0])
	}
}
