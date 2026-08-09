package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/config"
	"mcpx/internal/remotesession"
)

const cleanCoreCapabilityVersion = "clean-core-p4"

func capabilityGroups() map[string][]string {
	return map[string][]string{
		"core":    {"session", "read", "edit", "move_out", "observe", "execute", "plan", "artifact", "discover", "skill_call", "mcp_call"},
		"support": {"operation_batch", "operation_manage", "runtime_read", "environment_read", "environment", "screenshot_capture", "secret_provide"},
	}
}

type toolCapabilityDefinition struct {
	Name                  string
	Domain                string
	RequiresRemoteSession bool
	Roles                 []string
	Feature               string
}

// toolCapabilityDefinitions contains only capability metadata. The public name,
// description, schema, and annotations are authoritative in registerTools and
// are fingerprinted from the resulting MCP registration.
var toolCapabilityDefinitions = []toolCapabilityDefinition{
	{Name: "observe", Domain: "observation", RequiresRemoteSession: true},
	{Name: "operation_batch", Domain: "operation", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "operation_manage", Domain: "operation", RequiresRemoteSession: true},
	{Name: "session", Domain: "session"},
	{Name: "read", Domain: "source", RequiresRemoteSession: true},
	{Name: "edit", Domain: "edit", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "move_out", Domain: "workspace_move_out", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "execute", Domain: "command", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "terminal"},
	{Name: "plan", Domain: "plan", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "artifact", Domain: "artifact", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "discover", Domain: "extension", RequiresRemoteSession: true},
	{Name: "skill_call", Domain: "extension", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "skills"},
	{Name: "mcp_call", Domain: "extension", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "mcp"},
	// Support tools intentionally remain outside the core workflow but share
	// the same remote_session_id contract; their definitions are listed above.
	{Name: "runtime_read", Domain: "runtime"},
	{Name: "environment_read", Domain: "environment"},
	{Name: "environment", Domain: "environment", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "screenshot_capture", Domain: "screenshot", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "secret_provide", Domain: "secrets", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
}

func machineToolCapabilities(effective config.Config, session *remotesession.Session) []map[string]any {
	items := make([]map[string]any, 0, len(toolCapabilityDefinitions))
	for _, definition := range toolCapabilityDefinitions {
		state, reason := "available", ""
		switch definition.Feature {
		case "terminal":
			if !effective.Terminal.Enabled {
				state, reason = "disabled", "terminal_disabled"
			}
		case "file_watch":
			if !effective.FileWatch.Enabled {
				state, reason = "disabled", "file_watch_disabled"
			}
		case "mcp":
			if !effective.Discovery.MCP.Enabled {
				state, reason = "disabled", "mcp_discovery_disabled"
			}
		case "skills":
			if !effective.Discovery.Skills.Enabled {
				state, reason = "disabled", "skill_discovery_disabled"
			}
		}
		if state == "available" && definition.RequiresRemoteSession && session == nil {
			state, reason = "requires_remote_session", "session_id_required"
		}
		if state == "available" && session != nil && len(definition.Roles) > 0 && !containsString(definition.Roles, session.Role) {
			state, reason = "forbidden", "role_not_allowed"
		}
		item := map[string]any{
			"name": definition.Name, "domain": definition.Domain, "state": state,
			"requires_remote_session": definition.RequiresRemoteSession,
		}
		if len(definition.Roles) > 0 {
			item["roles"] = definition.Roles
		}
		if reason != "" {
			item["reason"] = reason
		}
		if limit, ok := publishedLimits()[definition.Name]; ok {
			item["limits"] = limit
		}
		items = append(items, item)
	}
	return items
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func capabilityRevision(data map[string]any) string {
	encoded, _ := json.Marshal(data)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func capabilityToolNames() []string {
	names := make([]string, 0, len(toolCapabilityDefinitions))
	for _, definition := range toolCapabilityDefinitions {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	return names
}

// revision helpers — hash any stable JSON-serializable value.
func hashRevision(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func skillRevision(skills any) string     { return hashRevision(skills) }
func mcpRevision(servers any) string      { return hashRevision(servers) }
func instructionRevision(docs any) string { return hashRevision(docs) }

func sessionCapabilityRevision(session *remotesession.Session) string {
	if session == nil {
		return "sha256:none"
	}
	return hashRevision(map[string]any{"id": session.ID, "role": session.Role, "status": session.Status})
}

// capabilityManifestRevision is independent of a single session role snapshot.
func capabilityManifestRevision(tools, skills, servers, instructions, guidance any) string {
	return hashRevision(map[string]any{
		"tools": tools, "skills": skills, "servers": servers, "instructions": instructions,
		"guidance": guidance,
	})
}

// registeredToolManifest is derived from the MCP server snapshot. Capability
// and documentation consumers therefore cannot silently drift from tools/list.
func (r *Runtime) registeredToolManifest() []map[string]any {
	tools := r.registeredTools()
	manifest := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			continue
		}
		var item map[string]any
		if json.Unmarshal(encoded, &item) == nil {
			manifest = append(manifest, item)
		}
	}
	return manifest
}

func summarizeToolManifest(manifest []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(manifest))
	for _, tool := range manifest {
		item := map[string]any{
			"name": tool["name"], "description": tool["description"], "annotations": tool["annotations"],
		}
		if safety := toolSafetyMetadata(tool); safety != nil {
			item["safety"] = safety
		}
		result = append(result, item)
	}
	return result
}

func toolSafetyMetadata(tool map[string]any) map[string]any {
	switch meta := tool["_meta"].(type) {
	case map[string]any:
		safety, _ := meta["mcpx/safety"].(map[string]any)
		return safety
	case mcp.Meta:
		safety, _ := meta["mcpx/safety"].(map[string]any)
		return safety
	default:
		return nil
	}
}

func (r *Runtime) runtimeToolCapabilities(effective config.Config, session *remotesession.Session, includeSchemas bool) []map[string]any {
	items := machineToolCapabilities(effective, session)
	manifest := r.registeredToolManifest()
	byName := make(map[string]map[string]any, len(manifest))
	for _, tool := range manifest {
		if name, ok := tool["name"].(string); ok {
			byName[name] = tool
		}
	}
	for _, item := range items {
		name, _ := item["name"].(string)
		if tool := byName[name]; tool != nil {
			item["description"] = tool["description"]
			if includeSchemas {
				item["input_schema"] = tool["inputSchema"]
			}
			item["annotations"] = tool["annotations"]
			if safety := toolSafetyMetadata(tool); safety != nil {
				item["safety"] = safety
			}
		}
	}
	return items
}
