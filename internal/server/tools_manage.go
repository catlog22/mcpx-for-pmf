package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/skill"
)

func toolAction(req *mcp.CallToolRequest) string {
	action, _ := mcpresult.Arguments(req)["action"].(string)
	return strings.ToLower(strings.TrimSpace(action))
}

func forwardedRequest(req *mcp.CallToolRequest, updates map[string]any) *mcp.CallToolRequest {
	args := make(map[string]any, len(mcpresult.Arguments(req))+len(updates))
	for key, value := range mcpresult.Arguments(req) {
		args[key] = value
	}
	for key, value := range updates {
		args[key] = value
	}
	return mcpresult.Request(args)
}

func (r *Runtime) invalidAction(ctx context.Context, req *mcp.CallToolRequest, tool, action string) (*mcp.CallToolResult, error) {
	envReq, _, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	publicToolName, publicArguments := normalizePublicAction(tool, map[string]any{"action": action})
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, envReq.Workspace, map[string]any{
		"tool":      publicToolName,
		"arguments": publicArguments,
	}, "INVALID_ACTION", fmt.Sprintf("%s does not support view or operation %q", publicToolName, action))
	return r.resultJSON(response)
}

func (r *Runtime) toolSessionManage(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "list":
		return r.toolRemoteSessionList(ctx, req)
	case "get":
		return r.toolRemoteSessionGet(ctx, req)
	case "events":
		return r.toolRemoteSessionEvents(ctx, req)
	case "update":
		return r.toolRemoteSessionUpdate(ctx, req)
	case "handoff":
		return r.toolRemoteSessionHandoff(ctx, req)
	case "attach":
		return r.toolRemoteSessionAttach(ctx, req)
	case "close":
		return r.toolRemoteSessionClose(ctx, req)
	default:
		return r.invalidAction(ctx, req, "session_manage", toolAction(req))
	}
}

func (r *Runtime) toolChangeManage(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "prepare":
		return r.toolChangePrepare(ctx, req)
	case "diff":
		return r.toolChangeDiff(ctx, req)
	case "history":
		return r.toolChangeHistory(ctx, req)
	case "discard":
		return r.toolChangeDiscard(ctx, req)
	default:
		return r.invalidAction(ctx, req, "change_manage", toolAction(req))
	}
}

func (r *Runtime) toolRuntimeInspect(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "capabilities":
		return r.toolCapabilityList(ctx, req)
	case "project":
		return r.toolProjectInspect(ctx, req)
	case "instructions":
		return r.toolAgentInstructionList(ctx, req)
	default:
		return r.invalidAction(ctx, req, "runtime_inspect", toolAction(req))
	}
}

func (r *Runtime) toolWorkspaceState(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "changes":
		return r.toolWorkspaceChanges(ctx, req)
	case "snapshot":
		return r.toolFileSnapshot(ctx, req)
	case "diff":
		return r.toolFileChanges(ctx, req)
	case "watch":
		return r.toolFileWatch(ctx, req)
	case "memory":
		return r.toolWorkspaceMemory(ctx, req)
	default:
		return r.invalidAction(ctx, req, "workspace_state", toolAction(req))
	}
}

func (r *Runtime) toolArtifactManage(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "list":
		return r.toolArtifactList(ctx, req)
	case "read":
		return r.toolArtifactRead(ctx, req)
	case "register":
		return r.toolArtifactRegister(ctx, req)
	default:
		return r.invalidAction(ctx, req, "artifact_manage", toolAction(req))
	}
}

// extension_manage intentionally lists both extension families in a single
// result. Calls still use the existing specialised handlers so their security,
// upstream errors, and audit events are preserved.
func (r *Runtime) toolExtensionManage(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	kind, _ := mcpresult.Arguments(req)["kind"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	query, _ := mcpresult.Arguments(req)["query"].(string)
	query = strings.TrimSpace(query)
	switch action {
	case "list":
		envReq, principal, fail := r.remoteRequest(ctx, req)
		if fail != nil {
			return fail, nil
		}
		workspace, remoteID, err := r.resolveExplicitWorkspace(ctx, principal, envReq)
		if err != nil {
			return r.remoteError(envReq, remoteID, workspace.Name, err)
		}
		effective := r.effectiveConfig(workspace.Path)
		data := map[string]any{}
		if kind == "" || kind == "skill" || kind == "skills" {
			skills := skill.LoadAll(effective.Discovery.Skills.Dirs, workspace.Path)
			data["skills"] = skillItems(filterSkillsByQuery(skills, query))
		}
		if kind == "" || kind == "mcp" {
			manager, managerErr := r.mcpManagerForWorkspace(workspace.Path)
			if managerErr != nil {
				return r.terminalError(envReq, remoteID, workspace.Name, "mcp_config_error", managerErr.Error())
			}
			servers := manager.List()
			servers = filterExtensionItemsByQuery(servers, query)
			if include, _ := mcpresult.Arguments(req)["include_tools"].(bool); include && effective.Discovery.MCP.Enabled {
				servers = r.enrichServersWithTools(ctx, manager, servers)
			}
			data["upstream_mcp"] = servers
		}
		if kind != "" && kind != "skill" && kind != "skills" && kind != "mcp" {
			return r.invalidAction(ctx, req, "extension_manage", kind)
		}
		if query != "" {
			data["query"] = query
		}
		return compactToolResult(data, "Extension inventory returned."), nil
	case "describe":
		if kind == "mcp" {
			name, _ := mcpresult.Arguments(req)["name"].(string)
			if name == "" {
				name, _ = mcpresult.Arguments(req)["server"].(string)
			}
			return r.toolMCPList(ctx, forwardedRequest(req, map[string]any{"server": name, "include_tools": true}))
		}
		if kind == "skill" || kind == "skills" {
			envReq, principal, fail := r.remoteRequest(ctx, req)
			if fail != nil {
				return fail, nil
			}
			workspace, remoteID, err := r.resolveExplicitWorkspace(ctx, principal, envReq)
			if err != nil {
				return r.remoteError(envReq, remoteID, workspace.Name, err)
			}
			name, _ := mcpresult.Arguments(req)["name"].(string)
			item, found := skill.Find(skill.LoadAll(r.effectiveConfig(workspace.Path).Discovery.Skills.Dirs, workspace.Path), name)
			if !found {
				return r.skillNotFound(envReq, remoteID, workspace.Name, name)
			}
			data := map[string]any{"skill": skillItems([]skill.Skill{item})[0]}
			return compactToolResult(data, fmt.Sprintf("Skill %s described.", name)), nil
		}
		return r.invalidAction(ctx, req, "extension_manage", kind)
	case "call":
		if kind == "mcp" {
			return r.toolMCPCall(ctx, req)
		}
		if kind == "skill" || kind == "skills" {
			return r.toolSkillExecute(ctx, req)
		}
		return r.invalidAction(ctx, req, "extension_manage", kind)
	default:
		return r.invalidAction(ctx, req, "extension_manage", action)
	}
}

func filterSkillsByQuery(skills []skill.Skill, query string) []skill.Skill {
	if strings.TrimSpace(query) == "" {
		return skills
	}
	filtered := make([]skill.Skill, 0, len(skills))
	for _, item := range skills {
		if extensionQueryMatches(query, item.Manifest.Name, item.Manifest.Description) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterExtensionItemsByQuery(items []map[string]any, query string) []map[string]any {
	if strings.TrimSpace(query) == "" {
		return items
	}
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		name, _ := item["name"].(string)
		description, _ := item["description"].(string)
		if extensionQueryMatches(query, name, description) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func extensionQueryMatches(query string, values ...string) bool {
	text := strings.ToLower(strings.Join(values, " "))
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func (r *Runtime) skillNotFound(envReq envelope.Request, remoteSessionID, workspace, name string) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, workspace, nil, "not_found", fmt.Sprintf("skill %q was not found; call extension_manage with action=list and kind=skill before selecting a name", strings.TrimSpace(name)))
	response.RemoteSessionID = remoteSessionID
	addRecoveryAction(&response, "extension_manage", "list configured Skills before selecting a name", map[string]any{
		"remote_session_id": remoteSessionID,
		"action":            "list",
		"kind":              "skill",
	})
	return r.resultJSON(response)
}
