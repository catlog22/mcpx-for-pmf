package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/mcpproxy"
	"mcpx/internal/mcpresult"
	"mcpx/internal/remotesession"
	"mcpx/internal/skill"
)

const discoveryLeaseTTL = 10 * time.Minute

type discoveryLease struct {
	ID              string
	Revision        string
	RemoteSessionID string
	PrincipalID     string
	WorkspacePath   string
	Kind            string
	Object          string
	ExpiresAt       time.Time
	ArgumentsSchema map[string]any
	MCPTools        []*mcp.Tool
}

func (r *Runtime) toolDiscover(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	req, _, _ = canonicalDiscoverRequest(req)
	envReq, principal, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	kind := strings.ToLower(strings.TrimSpace(stringPayload(envReq.Payload, "kind")))
	view := strings.ToLower(strings.TrimSpace(stringPayload(envReq.Payload, "view")))
	if view == "" {
		view = "list"
	}
	if kind != "skill" && kind != "mcp" {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "DISCOVERY_KIND_INVALID", "kind must be skill or mcp")
	}
	if view != "list" && view != "describe" {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "DISCOVERY_VIEW_INVALID", "view must be list or describe")
	}
	if kind == "skill" {
		return r.discoverSkill(ctx, envReq, principal.ID, session, view)
	}
	return r.discoverMCP(ctx, envReq, principal.ID, session, view)
}

func canonicalDiscoverRequest(req *mcp.CallToolRequest) (*mcp.CallToolRequest, string, string) {
	args := mcpresult.Arguments(req)
	kind := strings.ToLower(strings.TrimSpace(stringPayload(args, "kind")))
	view := strings.ToLower(strings.TrimSpace(stringPayload(args, "view")))
	server := strings.TrimSpace(stringPayload(args, "server"))
	name := strings.TrimSpace(stringPayload(args, "name"))
	includeTools := boolPayload(args, "include_tools")
	updates := map[string]any{}
	if kind == "" && (server != "" || includeTools) {
		kind = "mcp"
		updates["kind"] = kind
	}
	if view == "" {
		if server != "" || name != "" || includeTools {
			view = "describe"
		} else {
			view = "list"
		}
		updates["view"] = view
	}
	if len(updates) > 0 {
		req = forwardedRequest(req, updates)
	}
	return req, kind, view
}

func (r *Runtime) discoverSkill(ctx context.Context, envReq envelope.Request, principalID string, session remotesession.Session, view string) (*mcp.CallToolResult, error) {
	effective := r.effectiveConfig(session.WorkspacePath)
	if !effective.Discovery.Skills.Enabled {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "disabled", "skill discovery is disabled")
	}
	skills := skill.LoadAll(effective.Discovery.Skills.Dirs, session.WorkspacePath)
	query := strings.TrimSpace(stringPayload(envReq.Payload, "query"))
	name := strings.TrimSpace(stringPayload(envReq.Payload, "name"))
	if view == "list" && name == "" {
		items := skillItems(filterSkillsByQuery(skills, query))
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{
			"kind": "skill", "view": "list", "skills": items,
			"discovery_required_for_call": true,
		})
	}
	if name == "" {
		return r.discoveryTargetRequired(envReq, session, "skill", "name", "discover the selected Skill before calling it")
	}
	item, found := skill.Find(skills, name)
	if !found {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "SKILL_NOT_FOUND", fmt.Sprintf("skill %q was not found", name))
	}
	descriptor := skillItems([]skill.Skill{item})[0]
	revision, _ := descriptor["revision"].(string)
	lease := r.upsertDiscoveryLease(discoveryLease{
		Revision: revision, RemoteSessionID: session.ID, PrincipalID: principalID,
		WorkspacePath: session.WorkspacePath, Kind: "skill", Object: name,
		ArgumentsSchema: item.Manifest.ArgumentsSchema,
	})
	descriptor["discovery_id"] = lease.ID
	descriptor["discovery_revision"] = lease.Revision
	descriptor["expires_at"] = lease.ExpiresAt
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{
		"kind": "skill", "view": "describe", "skill": descriptor,
		"discovery_id": lease.ID, "discovery_revision": lease.Revision, "expires_at": lease.ExpiresAt,
		"invocation_template": map[string]any{
			"tool": "skill_call",
			"arguments": map[string]any{
				"remote_session_id": session.ID, "name": name, "arguments": map[string]any{},
				"discovery_id": lease.ID, "discovery_revision": lease.Revision,
			},
			"required_client_fields": []string{"purpose"},
		},
	})
}

func (r *Runtime) discoverMCP(ctx context.Context, envReq envelope.Request, principalID string, session remotesession.Session, view string) (*mcp.CallToolResult, error) {
	effective := r.effectiveConfig(session.WorkspacePath)
	if !effective.Discovery.MCP.Enabled {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "disabled", "MCP discovery is disabled")
	}
	manager, err := r.mcpManagerForWorkspace(session.WorkspacePath)
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "MCP_CONFIG_ERROR", err.Error())
	}
	serverName := strings.TrimSpace(stringPayload(envReq.Payload, "server"))
	if serverName == "" {
		serverName = strings.TrimSpace(stringPayload(envReq.Payload, "name"))
	}
	includeTools := boolPayload(envReq.Payload, "include_tools")
	if view == "list" && serverName == "" {
		items := filterExtensionItemsByQuery(manager.List(), strings.TrimSpace(stringPayload(envReq.Payload, "query")))
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{
			"kind": "mcp", "view": "list", "servers": items,
			"discovery_required_for_call": true,
		})
	}
	if serverName == "" {
		return r.discoveryTargetRequired(envReq, session, "mcp", "server", "discover the selected MCP server with include_tools=true before calling it")
	}
	if !includeTools {
		includeTools = true
	}
	serverConfig, found := manager.ServerConfig(serverName)
	if !found {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "MCP_SERVER_NOT_FOUND", fmt.Sprintf("MCP server %q is not configured", serverName))
	}
	tools, err := mcpproxy.ListTools(ctx, serverConfig)
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "MCP_DISCOVERY_FAILED", err.Error())
	}
	revision := mcpRevision(tools)
	lease := r.upsertDiscoveryLease(discoveryLease{
		Revision: revision, RemoteSessionID: session.ID, PrincipalID: principalID,
		WorkspacePath: session.WorkspacePath, Kind: "mcp", Object: serverName, MCPTools: tools,
	})
	server := map[string]any{"name": serverName, "tools": tools, "tools_revision": revision,
		"discovery_id": lease.ID, "discovery_revision": lease.Revision, "expires_at": lease.ExpiresAt}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, map[string]any{
		"kind": "mcp", "view": "describe", "server": server,
		"discovery_id": lease.ID, "discovery_revision": lease.Revision, "expires_at": lease.ExpiresAt,
		"invocation_template": map[string]any{
			"tool": "mcp_call",
			"arguments": map[string]any{
				"remote_session_id": session.ID, "server": serverName, "arguments": map[string]any{},
				"discovery_id": lease.ID, "discovery_revision": lease.Revision,
			},
			"required_client_fields": []string{"purpose", "tool"},
		},
	})
}

func (r *Runtime) upsertDiscoveryLease(input discoveryLease) discoveryLease {
	now := time.Now().UTC()
	r.discoveryMu.Lock()
	defer r.discoveryMu.Unlock()
	for key, existing := range r.discoveries {
		if existing.RemoteSessionID == input.RemoteSessionID && existing.PrincipalID == input.PrincipalID &&
			existing.WorkspacePath == input.WorkspacePath && existing.Kind == input.Kind && existing.Object == input.Object &&
			existing.Revision == input.Revision && existing.ExpiresAt.After(now) {
			existing.ExpiresAt = now.Add(discoveryLeaseTTL)
			r.discoveries[key] = existing
			return existing
		}
	}
	input.ID = newRuntimeID("disc", 12)
	input.ExpiresAt = now.Add(discoveryLeaseTTL)
	key := input.ID
	r.discoveries[key] = input
	return input
}

func (r *Runtime) discoveryLeaseFor(id string) (discoveryLease, bool) {
	r.discoveryMu.Lock()
	defer r.discoveryMu.Unlock()
	lease, ok := r.discoveries[strings.TrimSpace(id)]
	if !ok || !lease.ExpiresAt.After(time.Now().UTC()) {
		if ok {
			delete(r.discoveries, id)
		}
		return discoveryLease{}, false
	}
	return lease, true
}

func (r *Runtime) requireDiscovery(envReq envelope.Request, principalID string, session remotesession.Session, kind, object string) (discoveryLease, *mcp.CallToolResult) {
	id := strings.TrimSpace(stringPayload(envReq.Payload, "discovery_id"))
	revision := strings.TrimSpace(stringPayload(envReq.Payload, "discovery_revision"))
	if id == "" || revision == "" {
		result, _ := r.discoveryError(envReq, session, "DISCOVERY_REQUIRED", kind, object, "显式 discover 是调用资格的一部分；本次请求未发生隐式发现")
		return discoveryLease{}, result
	}
	lease, ok := r.discoveryLeaseFor(id)
	if !ok || lease.RemoteSessionID != session.ID || lease.PrincipalID != principalID || lease.WorkspacePath != session.WorkspacePath || lease.Kind != kind || lease.Object != object {
		code := "DISCOVERY_STALE"
		if kind == "mcp" {
			code = "MCP_DISCOVERY_STALE"
		}
		result, _ := r.discoveryError(envReq, session, code, kind, object, "discovery_id 不属于当前 Remote Session、principal、Workspace 或对象")
		return discoveryLease{}, result
	}
	if lease.Revision != revision {
		code := "DISCOVERY_STALE"
		if kind == "mcp" {
			code = "MCP_DISCOVERY_STALE"
		}
		result, _ := r.discoveryError(envReq, session, code, kind, object, "discovery_revision 已失效，请重新 discover")
		return discoveryLease{}, result
	}
	return lease, nil
}

func (r *Runtime) discoveryError(envReq envelope.Request, session remotesession.Session, code, kind, object, message string) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, code, message)
	response.RemoteSessionID = session.ID
	if response.Error != nil {
		response.Error.Details["required_call_count"] = 1
		response.Error.Details["discovery_required"] = true
		args := map[string]any{"remote_session_id": session.ID, "kind": kind, "view": "describe"}
		if kind == "skill" {
			args["name"] = object
		} else {
			args["server"] = object
			args["include_tools"] = true
		}
		addRecoveryAction(&response, "discover", "先显式 discover 当前对象，再原样复制 discovery_id 和 discovery_revision 调用", args)
	}
	return r.resultJSON(response)
}

func (r *Runtime) discoveryTargetRequired(envReq envelope.Request, session remotesession.Session, kind, field, reason string) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, "DISCOVERY_TARGET_REQUIRED", field+" is required")
	response.RemoteSessionID = session.ID
	if response.Error != nil {
		response.Error.Details["required_call_count"] = 1
		response.Error.Details["discovery_required"] = true
		addRecoveryAction(&response, "discover", reason, map[string]any{"remote_session_id": session.ID, "kind": kind, "view": "list"})
	}
	return r.resultJSON(response)
}

func (r *Runtime) toolSkillCallClean(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	return r.withCleanIdempotency(ctx, req, "skill_call", mcpresult.Arguments(req), r.toolSkillExecute, r.preflightCleanSkillCall)
}

func (r *Runtime) toolMCPCallClean(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	return r.withCleanIdempotency(ctx, req, "mcp_call", mcpresult.Arguments(req), r.toolMCPCall, r.preflightCleanMCPCall)
}

// preflightCleanSkillCall validates the explicit discovery lease and the
// current manifest without executing the Skill. It is deliberately side-effect
// free so idempotency Claim can happen only after the call is eligible.
func (r *Runtime) preflightCleanSkillCall(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	effective := r.effectiveConfig(remote.WorkspacePath)
	if !effective.Discovery.Skills.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "skill discovery is disabled")
	}
	name := strings.TrimSpace(stringPayload(envReq.Payload, "name"))
	skills := skill.LoadAll(effective.Discovery.Skills.Dirs, remote.WorkspacePath)
	sk, ok := skill.Find(skills, name)
	if !ok {
		return r.skillNotFound(envReq, remote.ID, remote.WorkspaceName, name)
	}
	current := skillItems([]skill.Skill{sk})[0]
	currentRevision, _ := current["revision"].(string)
	lease, discoveryFail := r.requireDiscovery(envReq, principal.ID, remote, "skill", name)
	if discoveryFail != nil {
		return discoveryFail, nil
	}
	if lease.Revision != currentRevision {
		result, _ := r.discoveryError(envReq, remote, "DISCOVERY_STALE", "skill", name, "Skill manifest revision changed after discover")
		return result, nil
	}
	arguments, argumentsOK := envReq.Payload["arguments"].(map[string]any)
	if raw, exists := envReq.Payload["arguments"]; exists && raw != nil && !argumentsOK {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "SKILL_ARGUMENTS_INVALID", "arguments must be an object")
	}
	if err := validateDiscoveryArguments(sk.Manifest.ArgumentsSchema, arguments); err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "SKILL_ARGUMENTS_INVALID", err.Error())
	}
	return nil, nil
}

// preflightCleanMCPCall performs all local checks needed before a durable
// idempotency claim. It never starts the upstream process or calls the tool.
func (r *Runtime) preflightCleanMCPCall(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	if !r.effectiveConfig(remote.WorkspacePath).Discovery.MCP.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "MCP discovery is disabled")
	}
	serverName := strings.TrimSpace(stringPayload(envReq.Payload, "server"))
	toolName := strings.TrimSpace(stringPayload(envReq.Payload, "tool"))
	lease, discoveryFail := r.requireDiscovery(envReq, principal.ID, remote, "mcp", serverName)
	if discoveryFail != nil {
		return discoveryFail, nil
	}
	upstream, found := mcpToolForLease(lease.MCPTools, toolName)
	if !found {
		result, _ := r.discoveryError(envReq, remote, "MCP_DISCOVERY_STALE", "mcp", serverName, fmt.Sprintf("MCP tool %q was not present in the discovered schema", toolName))
		return result, nil
	}
	arguments, argumentsOK := envReq.Payload["arguments"].(map[string]any)
	if raw, exists := envReq.Payload["arguments"]; exists && raw != nil && !argumentsOK {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "MCP_ARGUMENTS_INVALID", "arguments must be an object")
	}
	if err := validateDiscoveryArguments(discoverySchemaMap(upstream.InputSchema), arguments); err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "MCP_ARGUMENTS_INVALID", err.Error())
	}
	manager, err := r.mcpManagerForWorkspace(remote.WorkspacePath)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "mcp_config_error", err.Error())
	}
	if _, ok := manager.ServerConfig(serverName); !ok {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "not_found", "mcp server not configured")
	}
	return nil, nil
}
