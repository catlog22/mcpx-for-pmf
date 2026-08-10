package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/file"
	"mcpx/internal/filesnapshot"
	"mcpx/internal/logging"
	"mcpx/internal/mcpproxy"
	"mcpx/internal/secrets"
	"mcpx/internal/skill"
	"mcpx/internal/terminal"
)

func (r *Runtime) secretEnvFromPayload(remoteSessionID string, payload map[string]any) (env []string, hasPassword bool) {
	if pw, ok := payload["password"].(string); ok && pw != "" {
		inject, _ := payload["password_inject"].(string)
		env = append(env, secrets.OneShotEnv(pw, inject)...)
		hasPassword = true
		if cache, _ := payload["cache"].(bool); cache {
			r.secrets.Set(remoteSessionID, "password", pw, true, 0)
		}
	}
	if arr, ok := payload["secrets_once"].([]any); ok {
		for _, it := range arr {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			name, _ := m["name"].(string)
			val, _ := m["value"].(string)
			inject, _ := m["inject"].(string)
			if val == "" {
				continue
			}
			hasPassword = true
			env = append(env, secrets.OneShotEnv(val, inject)...)
			if name != "" {
				if cache, _ := payload["cache"].(bool); cache {
					r.secrets.Set(remoteSessionID, name, val, true, 0)
				}
			}
		}
	}
	if arr, ok := payload["secrets"].([]any); ok {
		for _, it := range arr {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			ref, _ := m["ref"].(string)
			inject, _ := m["inject"].(string)
			if v, ok := r.secrets.Get(remoteSessionID, ref); ok {
				env = append(env, secrets.OneShotEnv(v, inject)...)
				hasPassword = true
			}
		}
	}
	return env, hasPassword
}

func (r *Runtime) terminalError(envReq envelope.Request, remoteSessionID, workspace, code, message string) (*mcp.CallToolResult, error) {
	return r.terminalErrorWithCleanMode(envReq, remoteSessionID, workspace, code, message, false)
}

func (r *Runtime) terminalErrorForContext(ctx context.Context, envReq envelope.Request, remoteSessionID, workspace, code, message string) (*mcp.CallToolResult, error) {
	return r.terminalErrorWithCleanMode(envReq, remoteSessionID, workspace, code, message, isCleanCoreRequest(ctx))
}

func (r *Runtime) terminalErrorWithCleanMode(envReq envelope.Request, remoteSessionID, workspace, code, message string, clean bool) (*mcp.CallToolResult, error) {
	status := envelope.StatusError
	if code == "denied" || code == "disabled" || code == "forbidden" {
		status = envelope.StatusDenied
	}
	response := envelope.Fail(status, envReq.RequestID, workspace, nil, code, message)
	response.RemoteSessionID = remoteSessionID
	switch code {
	case "task_not_found", "task_list_error", "not_found":
		if strings.Contains(strings.ToLower(code+" "+message), "task") {
			if clean {
				addRecoveryAction(&response, "observe", "refresh the available Task identifiers before retrying", map[string]any{
					"remote_session_id": remoteSessionID,
					"view":              "status",
				})
			} else {
				addRecoveryAction(&response, "task_manage", "refresh the available Task identifiers before retrying", map[string]any{
					"remote_session_id": remoteSessionID,
					"action":            "list",
				})
			}
		}
	}
	return r.resultJSON(response)
}

func (r *Runtime) toolFileSnapshot(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	snap, err := file.TakeSnapshot(remote.WorkspacePath)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "snap_error", err.Error())
	}
	if err := r.fileSnapshots.Save(ctx, remote.ID, snap); err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "snap_error", err.Error())
	}
	return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{
		"snapshot_id": snap.ID,
		"at":          snap.At,
		"stats":       map[string]any{"files": len(snap.Hash)},
	})
}

func (r *Runtime) toolFileChanges(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	since, _ := envReq.Payload["since"].(string)
	since = strings.TrimSpace(since)
	if since == "" {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "snapshot_required", "snapshot_id is required; call workspace_observe with view=snapshot first")
	}
	old, err := r.fileSnapshots.Get(ctx, remote.ID, since)
	if err != nil {
		code := "snap_error"
		if errors.Is(err, filesnapshot.ErrNotFound) {
			code = "snapshot_not_found"
			err = fmt.Errorf("snapshot %q was not found; call workspace_observe with view=snapshot and retry with its snapshot_id", since)
		}
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, code, err.Error())
	}
	if old.WorkspaceRoot != remote.WorkspacePath {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "forbidden", "snapshot belongs to another workspace")
	}
	neu, err := file.TakeSnapshot(remote.WorkspacePath)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "snap_error", err.Error())
	}
	ch := file.DiffSnapshots(old, neu)
	return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"changes": ch})
}

func (r *Runtime) toolFileWatch(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	if !r.effectiveConfig(remote.WorkspacePath).FileWatch.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "file watch is disabled")
	}
	// Pull-based: same as changes against a stored snapshot.
	return r.toolFileChanges(ctx, req)
}

func (r *Runtime) toolSecretsProvide(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	sid := envReq.Payload["secret_id"]
	secretID, _ := sid.(string)
	values := map[string]string{}
	if m, ok := envReq.Payload["values"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				values[k] = s
			}
		}
	}
	if secretID == "" {
		refs := r.secrets.Cache(remote.ID, values)
		r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: firstString(toolInvocationName(ctx), "secret_provide"), Status: "ok", Detail: map[string]any{"refs": refs}})
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"cached_refs": refs, "persisted": false})
	}
	p, err := r.secrets.Provide(remote.ID, secretID, values)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "secret_error", err.Error())
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: firstString(toolInvocationName(ctx), "secret_provide"), Status: "ok", SecretID: secretID})
	if p.ResumeExec && p.Command != "" {
		res, err := terminal.Exec(ctx, terminal.ExecOptions{
			WorkDir:  p.WorkDir,
			Command:  p.Command,
			ExtraEnv: secrets.OneShotEnv(values["password"], "askpass"),
		})
		if err != nil && res.ExitCode == -1 {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "exec_error", err.Error())
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, r.capExecResult(res))
	}
	return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"secret_id": secretID, "cached_refs": sortedKeys(values), "persisted": false})
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r *Runtime) toolMCPList(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	ws, remoteID, err := r.resolveExplicitWorkspace(ctx, principal, envReq)
	if err != nil {
		return r.remoteError(envReq, remoteID, ws.Name, err)
	}
	manager, err := r.mcpManagerForWorkspace(ws.Path)
	if err != nil {
		return r.terminalError(envReq, remoteID, ws.Name, "mcp_config_error", err.Error())
	}
	servers := manager.List()
	data := map[string]any{"servers": servers}
	includeTools, _ := envReq.Payload["include_tools"].(bool)
	serverName, _ := envReq.Payload["server"].(string)
	serverName = strings.TrimSpace(serverName)
	if includeTools {
		if serverName != "" {
			srv, ok := manager.ServerConfig(serverName)
			if !ok {
				return r.terminalError(envReq, remoteID, ws.Name, "not_found", "mcp server not configured")
			}
			tools, discoverErr := mcpproxy.ListTools(ctx, srv)
			if discoverErr != nil {
				return r.terminalError(envReq, remoteID, ws.Name, "mcp_discovery_error", discoverErr.Error())
			}
			data["server"] = serverName
			data["tools"] = tools
			data["tools_revision"] = mcpRevision(tools)
		} else {
			// Discover tools for every configured server (A05 bootstrap path).
			data["servers"] = r.enrichServersWithTools(ctx, manager, servers)
			data["mcp_manifest_revision"] = mcpRevision(data["servers"])
		}
	}
	return r.remoteResult(envReq, remoteID, ws.Name, data)
}

func (r *Runtime) toolMCPCall(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	eff := r.effectiveConfig(remote.WorkspacePath)
	if !eff.Discovery.MCP.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "MCP discovery is disabled")
	}
	serverName, _ := envReq.Payload["server"].(string)
	toolName, _ := envReq.Payload["tool"].(string)
	args, _ := envReq.Payload["arguments"].(map[string]any)
	if isCleanCoreRequest(ctx) {
		if result, preflightErr := r.preflightCleanMCPCall(ctx, req); result != nil || preflightErr != nil {
			return result, preflightErr
		}
	}
	manager, err := r.mcpManagerForWorkspace(remote.WorkspacePath)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "mcp_config_error", err.Error())
	}
	cfg, ok := manager.ServerConfig(serverName)
	if !ok {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "not_found", "mcp server not configured")
	}
	res, err := mcpproxy.CallToolWithProgress(ctx, cfg, toolName, args, func(update mcpproxy.ToolProgress) {
		message := strings.TrimSpace(update.Message)
		if message == "" {
			message = "upstream tool is still running"
		}
		if update.Synthetic {
			message = "client heartbeat: " + message
		}
		if !notifyRequestProgress(ctx, req, fmt.Sprintf("MCP %s/%s: %s", serverName, toolName, message), update.Progress, update.Total) {
			logging.Debug("upstream mcp progress", "server", serverName, "tool", toolName, "message", message, "synthetic", update.Synthetic)
		}
	})
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "mcp_error", err.Error())
	}
	// serialize result
	b, _ := json.Marshal(res)
	maxResultBytes := config.MaxResultBytes(eff.Limits)
	if maxResultBytes > 0 && len(b) > maxResultBytes {
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, nil, "MCP_RESULT_TOO_LARGE", "upstream MCP result exceeds the configured response budget")
		response.RemoteSessionID = remote.ID
		if response.Error != nil {
			response.Error.Details["result_bytes"] = len(b)
			response.Error.Details["max_result_bytes"] = maxResultBytes
			addRecoveryAction(&response, "mcp_call", "使用上游工具的分页或 limit 参数缩小结果后重试", map[string]any{
				"remote_session_id": remote.ID, "server": serverName, "tool": toolName,
			})
		}
		return r.resultJSON(response)
	}
	var data any
	_ = json.Unmarshal(b, &data)
	if upstream, ok := res.(*mcp.CallToolResult); ok && upstream.IsError {
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, data, "MCP_CALL_FAILED", fmt.Sprintf("MCP %s/%s returned an error", serverName, toolName))
		response.RemoteSessionID = remote.ID
		return r.resultJSON(response)
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: firstString(toolInvocationName(ctx), "mcp_call"), Status: "ok", Detail: map[string]any{"server": serverName, "tool": toolName}})
	return compactToolResult(data, fmt.Sprintf("MCP %s/%s completed.", serverName, toolName)), nil
}

// mcpManagerForWorkspace returns a request-local immutable view. A shared
// manager cannot be reloaded per request because concurrent workspaces could
// otherwise observe each other's commands and environment variables.
func (r *Runtime) mcpManagerForWorkspace(wsPath string) (*mcpproxy.Manager, error) {
	var (
		file config.MCPFile
		err  error
	)
	if wsPath == "" {
		path, pathErr := config.GlobalMCPPath()
		if pathErr != nil {
			return nil, pathErr
		}
		file, err = config.LoadMCPFile(path)
	} else {
		file, err = config.LoadMergedMCP(wsPath)
	}
	if err != nil {
		return nil, err
	}
	return mcpproxy.NewManager(r.effectiveConfig(wsPath).Discovery.MCP.Enabled, file), nil
}

func skillItems(skills []skill.Skill) []map[string]any {
	sort.Slice(skills, func(i, j int) bool { return skills[i].Manifest.Name < skills[j].Manifest.Name })
	items := make([]map[string]any, 0, len(skills))
	for index, s := range skills {
		kind := "executable"
		forced := false
		if s.Manifest.Runtime == "markdown" || s.Manifest.Format == "skill_md" {
			kind = "instruction"
		}
		argumentsSchema := s.Manifest.ArgumentsSchema
		if len(argumentsSchema) == 0 {
			argumentsSchema = map[string]any{"type": "object", "additionalProperties": true}
		}
		// Stable content fingerprint for skill_manifest_revision (A06).
		revInput := s.Manifest.Name + "\x00" + s.Manifest.Description + "\x00" + s.Manifest.Runtime + "\x00" + s.Dir
		revision := skillRevision(revInput)
		items = append(items, map[string]any{
			"name": s.Manifest.Name, "description": s.Manifest.Description,
			"kind": kind, "runtime": s.Manifest.Runtime, "format": s.Manifest.Format,
			"permissions": s.Manifest.Permissions, "source": s.Source, "state": "available",
			"arguments_schema": argumentsSchema,
			"revision":         revision,
			"priority":         (index + 1) * 10,
			"required":         forced,
			"trigger":          map[string]any{"kind": "explicit", "tool": "discover"},
			"invocation": map[string]any{
				"tool": "skill_call", "requires_remote_session": true,
				"arguments": map[string]any{"name": s.Manifest.Name, "arguments": map[string]any{}, "discovery_required": true},
			},
		})
	}
	return items
}

func skillSummaryItems(skills []skill.Skill) []map[string]any {
	full := skillItems(skills)
	result := make([]map[string]any, 0, len(full))
	for _, item := range full {
		result = append(result, map[string]any{
			"name": item["name"], "description": item["description"], "kind": item["kind"],
			"runtime": item["runtime"], "format": item["format"], "state": item["state"],
			"revision": item["revision"], "required": item["required"],
		})
	}
	return result
}

func (r *Runtime) toolSkillExecute(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	name, _ := envReq.Payload["name"].(string)
	args := envReq.Payload["arguments"]
	eff := r.effectiveConfig(remote.WorkspacePath)
	if !eff.Discovery.Skills.Enabled {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "disabled", "skill discovery is disabled")
	}
	skills := skill.LoadAll(eff.Discovery.Skills.Dirs, remote.WorkspacePath)
	sk, ok := skill.Find(skills, name)
	if !ok {
		return r.skillNotFound(envReq, remote.ID, remote.WorkspaceName, name)
	}
	if isCleanCoreRequest(ctx) {
		if result, preflightErr := r.preflightCleanSkillCall(ctx, req); result != nil || preflightErr != nil {
			return result, preflightErr
		}
	}
	out, err := skill.Execute(ctx, sk, remote.WorkspacePath, args)
	if err != nil {
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, out, "skill_error", err.Error())
		response.RemoteSessionID = remote.ID
		return r.resultJSON(response)
	}
	if exitCode, ok := out["exit_code"].(int); ok && exitCode != 0 {
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, out, "SKILL_FAILED", fmt.Sprintf("Skill exited with code %d", exitCode))
		response.RemoteSessionID = remote.ID
		return r.resultJSON(response)
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: firstString(toolInvocationName(ctx), "skill_call"), Status: "ok", Detail: map[string]any{"name": name}})
	return compactToolResult(out, fmt.Sprintf("Skill %s completed.", name)), nil
}

func mcpToolInLease(tools []*mcp.Tool, wanted string) bool {
	_, ok := mcpToolForLease(tools, wanted)
	return ok
}

func mcpToolForLease(tools []*mcp.Tool, wanted string) (*mcp.Tool, bool) {
	for _, tool := range tools {
		if tool != nil && tool.Name == wanted {
			return tool, true
		}
	}
	return nil, false
}

func discoverySchemaMap(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if schema, ok := raw.(map[string]any); ok {
		return schema
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var schema map[string]any
	if json.Unmarshal(encoded, &schema) != nil {
		return nil
	}
	return schema
}

func validateDiscoveryArguments(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	if args == nil {
		args = map[string]any{}
	}
	if schemaType, _ := schema["type"].(string); schemaType != "" && schemaType != "object" {
		return fmt.Errorf("arguments schema must be an object, got %q", schemaType)
	}
	var required []string
	switch rawRequired := schema["required"].(type) {
	case []any:
		for _, raw := range rawRequired {
			if field, ok := raw.(string); ok {
				required = append(required, field)
			}
		}
	case []string:
		required = rawRequired
	}
	for _, field := range required {
		if field != "" {
			if _, exists := args[field]; !exists {
				return fmt.Errorf("missing required argument %q", field)
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	additional, _ := schema["additionalProperties"].(bool)
	if !additional && len(properties) > 0 {
		for key := range args {
			if _, exists := properties[key]; !exists {
				return fmt.Errorf("unknown argument %q", key)
			}
		}
	}
	for key, value := range args {
		property, _ := properties[key].(map[string]any)
		if property == nil {
			continue
		}
		if err := validateDiscoveryArgumentType(key, value, stringPayload(property, "type")); err != nil {
			return err
		}
	}
	return nil
}

func validateDiscoveryArgumentType(name string, value any, expected string) error {
	valid := true
	switch expected {
	case "string":
		_, valid = value.(string)
	case "number", "integer":
		switch value.(type) {
		case int, int64, float64, float32, json.Number:
		default:
			valid = false
		}
	case "boolean":
		_, valid = value.(bool)
	case "array":
		switch value.(type) {
		case []any, []string:
		default:
			valid = false
		}
	case "object":
		_, valid = value.(map[string]any)
	}
	if !valid {
		return fmt.Errorf("argument %q must be %s", name, expected)
	}
	return nil
}
