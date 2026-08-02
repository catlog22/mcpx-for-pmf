package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/instruction"
	"mcpx/internal/mcpproxy"
	"mcpx/internal/projecttask"
	"mcpx/internal/remotesession"
	"mcpx/internal/skill"
	buildversion "mcpx/internal/version"
)

// toolSessionOpen creates or reuses a Remote Session and returns a full bootstrap bundle
// so clients need only one MCP call to start developing.
func (r *Runtime) toolSessionOpen(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}

	includeInstrContent := false
	if v, ok := envReq.Payload["include_instructions_content"].(bool); ok {
		includeInstrContent = v
	}
	includeUpstreamTools, _ := envReq.Payload["include_upstream_tools"].(bool)
	includeProjectTasks := false
	if v, ok := envReq.Payload["include_project_tasks"].(bool); ok {
		includeProjectTasks = v
	}
	var session remotesession.Session
	remoteID, _ := envReq.Payload["remote_session_id"].(string)
	remoteID = strings.TrimSpace(remoteID)
	if remoteID == "" {
		remoteID = strings.TrimSpace(envReq.RemoteSessionID)
	}

	workspaceName := strings.TrimSpace(envReq.Workspace)
	if workspaceName == "" {
		workspaceName, _ = envReq.Payload["workspace"].(string)
	}

	if remoteID != "" {
		existing, err := r.remote.Get(ctx, principal, remoteID)
		if err != nil {
			return r.remoteError(envReq, remoteID, workspaceName, err)
		}
		session = existing
		workspaceName = session.WorkspaceName
	} else {
		created, err := r.createRemoteSession(ctx, principal, envReq, workspaceName)
		if err != nil {
			return r.remoteError(envReq, "", workspaceName, err)
		}
		session = created.Session
	}

	wsPath := session.WorkspacePath
	effective := r.effectiveConfig(wsPath)
	tools := r.runtimeToolCapabilities(effective, &session, false)

	servers := []map[string]any{}
	if manager, err := r.mcpManagerForWorkspace(wsPath); err == nil {
		servers = manager.List()
		if includeUpstreamTools && effective.Discovery.MCP.Enabled {
			servers = r.enrichServersWithTools(ctx, manager, servers)
		}
	}

	skills := []map[string]any{}
	if effective.Discovery.Skills.Enabled {
		skills = skillItems(skill.LoadAll(effective.Discovery.Skills.Dirs, wsPath))
	}

	docs := instruction.DiscoverAt(
		r.cfg.Discovery.Instructions.GlobalAgentsPath, wsPath, "",
		effective.Security.Files.MaxReadBytes,
	)
	var instructionPayload any
	if includeInstrContent {
		items, _ := instruction.ReadContents(docs, 256<<10)
		instructionPayload = map[string]any{"documents": items, "inline": true}
	} else {
		instructionPayload = map[string]any{"documents": docs, "inline": false}
	}

	project := inspectProject(ctx, wsPath)
	var tasks any
	if includeProjectTasks {
		tasks = projecttask.Discover(wsPath)
	}

	gitHead, treeDigest := workspaceRevision(ctx, wsPath)
	activeChangesets, _ := r.changesets.History(ctx, session.ID, 5)
	pendingApprovals := r.approvals.ListRemoteSession(session.ID)
	taskList, _ := r.tasks.List(session.ID, 20)
	artifacts, _ := r.artifacts.List(ctx, session.ID, "", 20)

	toolManifest := r.registeredToolManifest()
	build := r.build
	if build.Version == "" {
		build.Version = buildversion.Current
	}

	guidance := agentGuidance()
	revisions := map[string]any{
		"tool_schema_revision":         r.currentToolSchemaRevision(),
		"capability_manifest_revision": capabilityManifestRevision(toolManifest, skills, servers, docs, guidance),
		"guidance_revision":            agentGuidanceRevision(),
		"skill_revision":               skillRevision(skills),
		"mcp_revision":                 mcpRevision(servers),
		"instruction_revision":         instructionRevision(docs),
		"session_capability_revision":  sessionCapabilityRevision(&session),
		// Legacy aliases are retained in the payload for one migration cycle.
		"skill_manifest_revision": skillRevision(skills),
		"mcp_manifest_revision":   mcpRevision(servers),
	}

	data := map[string]any{
		"mcpx": map[string]any{
			"version": build.Version, "commit": build.Commit, "build_time": build.Date,
		},
		"remote_session": map[string]any{
			"id": session.ID, "role": session.Role, "status": session.Status,
			"version": session.Version, "label": session.Label, "description": session.Description,
			"workspace_name": session.WorkspaceName, "workspace_path": session.WorkspacePath,
		},
		"workspace": map[string]any{
			"name": session.WorkspaceName, "path": session.WorkspacePath,
			"git_head": gitHead, "tree_digest": treeDigest,
		},
		"revisions":      revisions,
		"agent_guidance": guidance,
		"tools":          tools,
		"skills":         map[string]any{"enabled": effective.Discovery.Skills.Enabled, "items": skills},
		"upstream_mcp":   map[string]any{"enabled": effective.Discovery.MCP.Enabled, "servers": servers},
		"instructions":   instructionPayload,
		"project":        project,
		"project_tasks":  tasks,
		"git": map[string]any{
			"head": gitHead, "tree_digest": treeDigest,
		},
		"active_changesets": activeChangesets,
		"pending_approvals": pendingApprovals,
		"tasks":             taskList,
		"artifacts":         artifacts,
		"schema_source":     "tools/list",
		"client_refresh":    clientRefreshPayload(envReq.Payload, revisions),
		"recommended_workflows": map[string]any{
			"bootstrap":     []string{"session_open"},
			"source_change": []string{"context_query", "file_read", "change_execute", "command_execute"},
		},
		"opened_at": time.Now().UTC().Format(time.RFC3339),
	}
	r.omitUnchangedSessionCapabilities(envReq.Payload, data, revisions)

	r.logAudit(audit.Event{
		RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName,
		Tool: "session_open", Status: "ok",
	})
	return compactToolResult(data, fmt.Sprintf("Session %s opened for workspace %s.", session.ID, session.WorkspaceName)), nil
}

func knownRevision(known map[string]any, key string) string {
	value, ok := known[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func clientRefreshPayload(payload map[string]any, revisions map[string]any) map[string]any {
	current := fmt.Sprint(revisions["tool_schema_revision"])
	currentGuidance := fmt.Sprint(revisions["guidance_revision"])
	known, _ := payload["known_revisions"].(map[string]any)
	previous := knownRevision(known, "tool_schema_revision")
	previousGuidance := knownRevision(known, "guidance_revision")
	toolChanged := previous == "" || previous != current
	guidanceChanged := previousGuidance != "" && previousGuidance != currentGuidance
	changed := toolChanged || guidanceChanged
	reason := "tool_schema_revision_changed"
	if !toolChanged && guidanceChanged {
		reason = "agent_guidance_changed"
	}
	result := map[string]any{
		"required":             changed,
		"reason":               reason,
		"tool_schema_revision": current,
		"guidance_revision":    currentGuidance,
	}
	if changed {
		result["actions"] = []string{"reconnect", "tools/list", "session_open"}
	} else {
		result["actions"] = []string{}
	}
	return result
}

func (r *Runtime) omitUnchangedSessionCapabilities(payload map[string]any, data, revisions map[string]any) {
	known, _ := payload["known_revisions"].(map[string]any)
	if len(known) == 0 {
		return
	}
	omitted := make([]string, 0, 4)
	omit := func(revisionKey, dataKey string) {
		if fmt.Sprint(known[revisionKey]) != "" && fmt.Sprint(known[revisionKey]) == fmt.Sprint(revisions[revisionKey]) {
			data[dataKey] = map[string]any{"omitted": true, "revision": revisions[revisionKey]}
			omitted = append(omitted, dataKey)
		}
	}
	omit("skill_revision", "skills")
	omit("mcp_revision", "upstream_mcp")
	omit("instruction_revision", "instructions")
	omit("session_capability_revision", "tools")
	if len(omitted) > 0 {
		data["omitted_sections"] = omitted
	}
}

func (r *Runtime) enrichServersWithTools(ctx context.Context, manager *mcpproxy.Manager, servers []map[string]any) []map[string]any {
	out := make([]map[string]any, len(servers))
	if len(servers) == 0 {
		return out
	}
	workerCount := len(servers)
	if workerCount > 4 {
		workerCount = 4
	}
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wait.Done()
			for index := range jobs {
				server := servers[index]
				item := make(map[string]any, len(server)+3)
				for key, value := range server {
					item[key] = value
				}
				name, _ := server["name"].(string)
				cfg, ok := manager.ServerConfig(name)
				if !ok {
					item["tools_error"] = "not_configured"
					out[index] = item
					continue
				}
				discoverCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				tools, err := mcpproxy.ListTools(discoverCtx, cfg)
				cancel()
				if err != nil {
					item["tools_error"] = err.Error()
					item["tools"] = []any{}
				} else {
					serialized := make([]map[string]any, 0, len(tools))
					for _, tool := range tools {
						raw, _ := json.Marshal(tool)
						var asMap map[string]any
						_ = json.Unmarshal(raw, &asMap)
						serialized = append(serialized, asMap)
					}
					item["tools"] = serialized
					item["tools_revision"] = mcpRevision(serialized)
					item["tools_error"] = nil
				}
				out[index] = item
			}
		}()
	}
	for index := range servers {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
	return out
}
