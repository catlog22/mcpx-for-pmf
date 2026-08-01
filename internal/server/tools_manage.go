package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/skill"
)

func toolAction(req mcp.CallToolRequest) string {
	action, _ := req.GetArguments()["action"].(string)
	return strings.ToLower(strings.TrimSpace(action))
}

func forwardedRequest(req mcp.CallToolRequest, updates map[string]any) mcp.CallToolRequest {
	out := req
	args := make(map[string]any, len(req.GetArguments())+len(updates))
	for key, value := range req.GetArguments() {
		args[key] = value
	}
	for key, value := range updates {
		args[key] = value
	}
	out.Params.Arguments = args
	return out
}

func (r *Runtime) invalidAction(ctx context.Context, req mcp.CallToolRequest, tool, action string) (*mcp.CallToolResult, error) {
	envReq, _, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, envReq.Workspace, map[string]any{
		"tool": tool,
	}, "INVALID_ACTION", fmt.Sprintf("%s does not support action %q", tool, action))
	return r.resultJSON(response)
}

func (r *Runtime) toolSessionManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func (r *Runtime) toolChangeManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "prepare":
		return r.toolChangePrepare(ctx, req)
	case "diff":
		return r.toolChangeDiff(ctx, req)
	case "history":
		return r.toolChangeHistory(ctx, req)
	default:
		return r.invalidAction(ctx, req, "change_manage", toolAction(req))
	}
}

func (r *Runtime) toolRuntimeInspect(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func (r *Runtime) toolWorkspaceState(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "changes":
		return r.toolWorkspaceChanges(ctx, req)
	case "snapshot":
		return r.toolFileSnapshot(ctx, req)
	case "diff":
		return r.toolFileChanges(ctx, req)
	case "watch":
		return r.toolFileWatch(ctx, req)
	default:
		return r.invalidAction(ctx, req, "workspace_state", toolAction(req))
	}
}

func (r *Runtime) toolArtifactManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func (r *Runtime) toolApprovalManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	switch toolAction(req) {
	case "list":
		return r.toolApprovalList(ctx, req)
	case "approve":
		return r.toolApprovalConfirm(ctx, forwardedRequest(req, map[string]any{"approve": true}))
	case "deny":
		return r.toolApprovalConfirm(ctx, forwardedRequest(req, map[string]any{"approve": false}))
	default:
		return r.invalidAction(ctx, req, "approval_manage", toolAction(req))
	}
}

// extension_manage intentionally lists both extension families in a single
// result. Calls still use the existing specialised handlers so their security,
// upstream errors, and audit events are preserved.
func (r *Runtime) toolExtensionManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	kind, _ := req.GetArguments()["kind"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
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
			data["skills"] = skillItems(skill.LoadAll(effective.Discovery.Skills.Dirs, workspace.Path))
		}
		if kind == "" || kind == "mcp" {
			manager, managerErr := r.mcpManagerForWorkspace(workspace.Path)
			if managerErr != nil {
				return r.terminalError(envReq, remoteID, workspace.Name, "mcp_config_error", managerErr.Error())
			}
			servers := manager.List()
			if include, _ := req.GetArguments()["include_tools"].(bool); include && effective.Discovery.MCP.Enabled {
				servers = r.enrichServersWithTools(ctx, manager, servers)
			}
			data["upstream_mcp"] = servers
		}
		if kind != "" && kind != "skill" && kind != "skills" && kind != "mcp" {
			return r.invalidAction(ctx, req, "extension_manage", kind)
		}
		return compactToolResult(data, "Extension inventory returned."), nil
	case "describe":
		if kind == "mcp" {
			name, _ := req.GetArguments()["name"].(string)
			if name == "" {
				name, _ = req.GetArguments()["server"].(string)
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
			name, _ := req.GetArguments()["name"].(string)
			item, found := skill.Find(skill.LoadAll(r.effectiveConfig(workspace.Path).Discovery.Skills.Dirs, workspace.Path), name)
			if !found {
				return r.terminalError(envReq, remoteID, workspace.Name, "not_found", "skill not found")
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
