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
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, workspace, nil, "not_found", fmt.Sprintf("skill %q was not found; use discover(kind=skill, view=list) before selecting a name", strings.TrimSpace(name)))
	response.RemoteSessionID = remoteSessionID
	addRecoveryAction(&response, "discover", "list configured Skills before selecting a name", map[string]any{
		"remote_session_id": remoteSessionID,
		"kind":              "skill",
		"view":              "list",
	})
	return r.resultJSON(response)
}
