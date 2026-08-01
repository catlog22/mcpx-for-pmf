package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfigUsesTransportSessionTTL(t *testing.T) {
	encoded, err := yaml.Marshal(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, "transport:\n    session_idle_ttl: 24h") {
		t.Fatalf("transport config missing:\n%s", text)
	}
	if strings.Contains(text, "\nsession:") {
		t.Fatalf("legacy session config must not be emitted:\n%s", text)
	}
}

func TestMergeKeepsGlobalTokenAndProjectDescription(t *testing.T) {
	g := DefaultConfig()
	g.Auth.Token = "global-tok"
	g.Discovery.Instructions.GlobalAgentsPath = "/trusted/AGENTS.md"
	p := Config{
		Auth:        AuthConfig{Token: "proj-tok"},
		Description: "proj desc",
		Discovery: DiscoveryConfig{Instructions: InstructionsDiscovery{
			GlobalAgentsPath: "/untrusted/AGENTS.md",
		}},
	}
	m := Merge(g, p)
	if m.Auth.Token != "global-tok" {
		t.Fatalf("token: %q", m.Auth.Token)
	}
	if m.Description != "proj desc" {
		t.Fatalf("desc: %q", m.Description)
	}
	if m.Discovery.Instructions.GlobalAgentsPath != "/trusted/AGENTS.md" {
		t.Fatalf("project replaced global instruction path: %q", m.Discovery.Instructions.GlobalAgentsPath)
	}
}

func TestLoadGlobalInstructionPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("discovery:\n  instructions:\n    global_agents_path: ~/AGENTS.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Discovery.Instructions.GlobalAgentsPath != "~/AGENTS.md" {
		t.Fatalf("global_agents_path: %q", cfg.Discovery.Instructions.GlobalAgentsPath)
	}
}

func TestMergeExplicitFalseFeatureFlags(t *testing.T) {
	global := DefaultConfig()
	var project Config
	if err := yaml.Unmarshal([]byte(`
terminal:
  enabled: false
file_watch:
  enabled: false
discovery:
  mcp:
    enabled: false
  skills:
    enabled: false
logging:
  enabled: false
`), &project); err != nil {
		t.Fatal(err)
	}
	merged := Merge(global, project)
	if merged.Terminal.Enabled || merged.FileWatch.Enabled ||
		merged.Discovery.MCP.Enabled || merged.Discovery.Skills.Enabled || merged.Logging.Enabled {
		t.Fatalf("explicit false flags not preserved: %+v", merged)
	}
}

func TestValidateSecurityRulesRejectsInvalidRegexp(t *testing.T) {
	err := ValidateSecurityRules(SecurityConfig{Commands: CommandRules{Deny: []string{"["}}})
	if err == nil {
		t.Fatal("expected invalid regexp error")
	}
}

func TestMergeCommandsReplace(t *testing.T) {
	g := DefaultConfig()
	p := Config{
		Security: SecurityConfig{
			Commands: CommandRules{Allow: []string{`^echo`}},
		},
	}
	m := Merge(g, p)
	if len(m.Security.Commands.Allow) != 1 || m.Security.Commands.Allow[0] != `^echo` {
		t.Fatalf("allow: %+v", m.Security.Commands.Allow)
	}
	if len(m.Security.Commands.Deny) != 0 {
		t.Fatalf("deny should be replaced empty, got %+v", m.Security.Commands.Deny)
	}
}

func TestLoadGlobalMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadGlobal(filepath.Join(dir, "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9090 {
		t.Fatalf("defaults: %+v", cfg.Server)
	}
}

func TestRegisterWorkspaceAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCPX_HOME", dir)
	global := filepath.Join(dir, "config.yaml")
	ws := filepath.Join(dir, "myproj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RegisterWorkspace(global, ws); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobal(global)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Workspaces) != 1 || cfg.Workspaces[0].Name != "myproj" {
		t.Fatalf("workspaces: %+v", cfg.Workspaces)
	}
	// second register updates same
	if err := RegisterWorkspace(global, ws); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadGlobal(global)
	if len(cfg.Workspaces) != 1 {
		t.Fatalf("dup: %+v", cfg.Workspaces)
	}
}

func TestMergeMCP(t *testing.T) {
	g := MCPFile{MCPServers: map[string]MCPServer{
		"github": {Command: "g", Type: "stdio"},
	}}
	p := MCPFile{MCPServers: map[string]MCPServer{
		"github": {Command: "g2", Type: "stdio"},
		"local":  {Command: "l", Type: "stdio"},
	}}
	m := MergeMCP(g, p)
	if m.MCPServers["github"].Command != "g2" {
		t.Fatal("project should override")
	}
	if _, ok := m.MCPServers["local"]; !ok {
		t.Fatal("project add")
	}
}

func TestLoadMCPFileMissing(t *testing.T) {
	f, err := LoadMCPFile(filepath.Join(t.TempDir(), "no.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.MCPServers) != 0 {
		t.Fatal("expected empty")
	}
}
