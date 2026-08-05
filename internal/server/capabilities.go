package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mcpx/internal/config"
	"mcpx/internal/remotesession"
)

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
	{Name: "workspace_list", Domain: "workspace"},
	{Name: "workspace_observe", Domain: "workspace", RequiresRemoteSession: true},
	{Name: "workspace_history_read", Domain: "workspace"},
	{Name: "operation_batch", Domain: "operation", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "operation_manage", Domain: "operation", RequiresRemoteSession: true},
	{Name: "session_open", Domain: "session"},
	{Name: "session_read", Domain: "session"},
	{Name: "session_transition", Domain: "session", Roles: []string{"owner", "editor"}},
	{Name: "source_read", Domain: "source", RequiresRemoteSession: true},
	{Name: "change_prepare", Domain: "change", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "change_read", Domain: "change", RequiresRemoteSession: true},
	{Name: "change_apply", Domain: "change", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "change_revert", Domain: "change", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "command_run", Domain: "command", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "terminal"},
	{Name: "task_read", Domain: "task", RequiresRemoteSession: true, Feature: "terminal"},
	{Name: "task_control", Domain: "task", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "terminal"},
	{Name: "progress_report", Domain: "observability", RequiresRemoteSession: true},
	{Name: "plan_create", Domain: "plan", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "plan_read", Domain: "plan", RequiresRemoteSession: true},
	{Name: "plan_transition", Domain: "plan", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "runtime_read", Domain: "runtime"},
	{Name: "environment_read", Domain: "environment"},
	{Name: "environment_snapshot_create", Domain: "environment", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
	{Name: "extension_discover", Domain: "extension"},
	{Name: "skill_call", Domain: "extension", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "skills"},
	{Name: "mcp_call", Domain: "extension", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}, Feature: "mcp"},
	{Name: "artifact_read", Domain: "artifact", RequiresRemoteSession: true},
	{Name: "artifact_register", Domain: "artifact", RequiresRemoteSession: true, Roles: []string{"owner", "editor"}},
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
		result = append(result, map[string]any{
			"name": tool["name"], "description": tool["description"], "annotations": tool["annotations"],
		})
	}
	return result
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
		}
	}
	return items
}
