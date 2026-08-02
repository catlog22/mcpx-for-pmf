package server

import (
	"encoding/json"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/environment"
)

// registerTools is the sole public tool registration point. The legacy
// fine-grained handlers remain private implementation details behind the
// compact 18-tool catalog below.
func (r *Runtime) registerTools(s *mcpserver.MCPServer) {
	r.registerConsolidatedTools(s)
	r.captureToolIndex(s)
}

func (r *Runtime) captureToolIndex(s *mcpserver.MCPServer) {
	listed := s.ListTools()
	index := make(map[string]mcp.Tool, len(listed))
	for name, registered := range listed {
		index[name] = registered.Tool
	}
	r.toolIndexMu.Lock()
	r.toolIndex = index
	r.toolIndexMu.Unlock()
}

func (r *Runtime) registeredTools() []mcp.Tool {
	r.toolIndexMu.RLock()
	defer r.toolIndexMu.RUnlock()
	tools := make([]mcp.Tool, 0, len(r.toolIndex))
	for _, tool := range r.toolIndex {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// currentToolSchemaRevision is derived from the actual MCP registration,
// including name, description, input schema, and annotations. It deliberately
// excludes Session state so opening or handing off a session cannot refresh a
// client's tools/list cache.
func (r *Runtime) currentToolSchemaRevision() string {
	tools := r.registeredTools()
	items := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded, _ := json.Marshal(tool)
		var item map[string]any
		_ = json.Unmarshal(encoded, &item)
		items = append(items, item)
	}
	return hashRevision(items)
}

// compactToolResult keeps the unstructured content concise. The public ARC
// wrapper moves the complete machine-readable result to response metadata.
func compactToolResult(data any, summary string) *mcp.CallToolResult {
	return mcp.NewToolResultStructured(data, summary)
}

type toolAnnotation struct {
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

var (
	readOnlyToolAnnotation = toolAnnotation{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}
	mutatingToolAnnotation = toolAnnotation{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: true}
	sessionToolAnnotation  = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
	secretToolAnnotation   = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
	// commandExecutionToolAnnotation: whether a command is destructive is
	// decided by the server-side command policy, not by the tool itself, so
	// hosts must not gate the call on the destructive hint.
	commandExecutionToolAnnotation = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: true}
	// approvalToolAnnotation: approve/deny only authorizes; it neither edits
	// files nor runs commands, so hosts must not gate it on a destructive hint.
	approvalToolAnnotation = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
)

func annotatedTool(tool mcp.Tool, annotation toolAnnotation) mcp.Tool {
	tool.Annotations.ReadOnlyHint = mcp.ToBoolPtr(annotation.ReadOnly)
	tool.Annotations.DestructiveHint = mcp.ToBoolPtr(annotation.Destructive)
	tool.Annotations.IdempotentHint = mcp.ToBoolPtr(annotation.Idempotent)
	tool.Annotations.OpenWorldHint = mcp.ToBoolPtr(annotation.OpenWorld)
	return tool
}

type actionSchemaBranch struct {
	Description string
	Properties  map[string]any
	Required    []string
}

func actionTool(name, description string, common map[string]any, branches map[string]actionSchemaBranch) mcp.Tool {
	return actionToolWithAnnotation(name, description, common, branches, mutatingToolAnnotation)
}

func actionToolWithAnnotation(name, description string, common map[string]any, branches map[string]actionSchemaBranch, annotation toolAnnotation) mcp.Tool {
	actions := make([]string, 0, len(branches))
	for action := range branches {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	properties := map[string]any{"action": map[string]any{"type": "string", "enum": actions}}
	for key, value := range common {
		properties[key] = value
	}
	oneOf := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		branch := branches[action]
		for key, value := range branch.Properties {
			if _, exists := properties[key]; !exists {
				properties[key] = value
			}
		}
		branchProperties := map[string]any{"action": map[string]any{"const": action}}
		for key, value := range branch.Properties {
			branchProperties[key] = value
		}
		required := append([]string{"action"}, branch.Required...)
		branchSchema := map[string]any{
			"properties": branchProperties,
			"required":   required,
		}
		description := branch.Description
		if description == "" {
			description = "Use this action only for the " + action + " operation; follow any next_action returned on failure."
		}
		branchSchema["description"] = description
		oneOf = append(oneOf, branchSchema)
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": []string{"action"}, "oneOf": oneOf,
	})
	tool := annotatedTool(mcp.NewTool(name, mcp.WithDescription(description)), annotation)
	tool.InputSchema = mcp.ToolInputSchema{}
	tool.RawInputSchema = raw
	return tool
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numberSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func arraySchema(items map[string]any, description string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": description}
}

func changeOperationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation":   map[string]any{"type": "string", "enum": []string{"update", "create", "rename", "delete", "replace_exact", "insert_before", "insert_after", "delete_exact", "replace_range"}, "description": "create: new file (content); update: apply_patch-style diff (patch); rename: move to new_path; delete: remove path; replace_exact: swap exact match text; insert_before/insert_after: append around a match; delete_exact: remove exact match; replace_range: rewrite line range"},
			"path":        map[string]any{"type": "string", "description": "workspace-relative target path (or source path for rename)"},
			"new_path":    map[string]any{"type": "string", "description": "rename destination path; only used with operation=rename"},
			"base_sha256": map[string]any{"type": "string", "description": "current file revision returned by file_read; omit only when creating a new file"},
			"patch":       map[string]any{"type": "string", "description": "apply_patch-style unified diff for operation=update"},
			"content":     map[string]any{"type": "string", "description": "full new file content for operation=create; keep each call at most 300 added lines and append to large files with insert_after; never embed instruction text into file content"},
			"match":       map[string]any{"type": "string", "description": "anchor text for replace_exact, insert_before, insert_after, or delete_exact"},
			"replacement": map[string]any{"type": "string", "description": "replacement text for replace_exact or replace_range"},
			"occurrence":  map[string]any{"type": "string", "enum": []string{"one"}, "description": "replace only the first match (default)"},
			"range_start": map[string]any{"type": "number", "description": "1-based start line for replace_range"},
			"range_end":   map[string]any{"type": "number", "description": "1-based end line for replace_range (inclusive)"},
		},
		"required": []string{"operation", "path"},
	}
}

func changeExecuteInputSchema() map[string]any {
	operations := changeOperationSchema()
	return map[string]any{
		"type":        "object",
		"description": "Sole file-modification entry with three mutually exclusive modes: (1) operations creates a new Changeset; (2) changeset_id + expected_digest applies a prepared draft; (3) revert_changeset_id reverts an applied Changeset. Send exactly one mode per call.",
		"properties": map[string]any{
			"remote_session_id":   stringSchema("persistent Remote Session identifier"),
			"summary":             stringSchema("change summary"),
			"operations":          map[string]any{"type": "array", "description": "apply_patch-style file operations; required for a new change; keep at most 300 added lines per call and build large files with create + insert_after appends; never embed instruction text into file content", "items": operations, "minItems": 1},
			"changeset_id":        stringSchema("prepared draft Changeset to apply through this same mutation entry"),
			"expected_digest":     stringSchema("digest returned when the prepared Changeset was created"),
			"revert_changeset_id": stringSchema("applied Changeset to revert through this same mutation entry"),
			"idempotency_key":     stringSchema("business idempotency key for retrying the same change"),
			"apply":               booleanSchema("apply after prepare; defaults to true"),
			"format":              booleanSchema("format changed files"),
			"verify":              arraySchema(map[string]any{"type": "string"}, "format, typecheck, lint, or related_tests"),
		},
		"required": []string{"remote_session_id"},
	}
}

func (r *Runtime) registerConsolidatedTools(s *mcpserver.MCPServer) {
	remoteSession := mcp.WithString("remote_session_id", mcp.Description("persistent Remote Session identifier"))
	commandRemoteSession := mcp.WithString("remote_session_id", mcp.Required(), mcp.Description("persistent Remote Session identifier"))
	workspace := mcp.WithString("workspace", mcp.Description("registered workspace name"))
	operationItems := mcp.Items(changeOperationSchema())

	// High-frequency development tools.
	r.addTool(s, annotatedTool(mcp.NewTool("workspace_list", mcp.WithDescription("List registered workspaces so the agent can choose a valid workspace before session_open. Use this as the first step when no workspace is known; it does not create, modify, or inspect files. After success, pass the returned workspace name to session_open.")), readOnlyToolAnnotation), r.toolWorkspaceList)
	r.addTool(s, annotatedTool(mcp.NewTool("session_open",
		mcp.WithDescription("Create or resume a Remote Session and return the bootstrap context for development. Use after workspace_list or with a known remote_session_id; it does not read files, edit files, or execute commands. On a missing workspace, call workspace_list and retry with its exact name."),
		remoteSession, workspace,
		mcp.WithString("label", mcp.Description("session label")),
		mcp.WithString("description", mcp.Description("development goal")),
		mcp.WithString("client_request_id", mcp.Description("idempotency key")),
		mcp.WithBoolean("include_instructions_content", mcp.Description("include bounded AGENTS.md content")),
		mcp.WithBoolean("include_upstream_tools", mcp.Description("discover upstream tool schemas")),
		mcp.WithBoolean("include_project_tasks", mcp.Description("include discovered project tasks")),
		mcp.WithObject("known_revisions", mcp.Description("previous revisions; unchanged capability payloads are omitted"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
	), sessionToolAnnotation), r.toolSessionOpen)
	r.addTool(s, annotatedTool(mcp.NewTool("file_read",
		mcp.WithDescription("Read one or more bounded workspace-relative file windows, including non-source files. Use before generating a change_execute request and after a revision conflict; do not use it to run shell commands or modify files. If a path is missing, follow the returned context_query recovery action."), remoteSession,
		mcp.WithString("path", mcp.Description("workspace-relative path for a single read")),
		mcp.WithNumber("offset", mcp.Description("zero-based line offset")),
		mcp.WithNumber("limit", mcp.Description("line count")),
		mcp.WithArray("items", mcp.Description("batch items: {path, offset, limit}"), mcp.Items(map[string]any{
			"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "number"}, "limit": map[string]any{"type": "number"},
			}, "required": []string{"path"},
		}), mcp.MinItems(1)),
		mcp.WithNumber("max_total_bytes", mcp.Description("total batch content budget")),
	), readOnlyToolAnnotation), r.toolFileReadUnified)
	r.addTool(s, annotatedTool(mcp.NewTool("context_query",
		mcp.WithDescription("Search, list, and assemble bounded workspace context; action selects the operation. Use to locate files or symbols when the path is unknown, not to execute commands or edit files. A truncated result carries the next context_query call needed to continue."), remoteSession,
		mcp.WithString("action", mcp.Required(), mcp.Enum("query", "search", "list")),
		mcp.WithString("query", mcp.Description("user search query")),
		mcp.WithString("mode", mcp.Enum("smart", "exact", "token"), mcp.Description("search mode; defaults to smart")),
		mcp.WithBoolean("parallel", mcp.Description("run exact and token recall concurrently; defaults to true")),
		mcp.WithNumber("max_results", mcp.Description("maximum ranked files; defaults to 20")),
		mcp.WithArray("paths", mcp.Description("seed paths or directory scopes"), mcp.WithStringItems()),
		mcp.WithString("include_glob", mcp.Description("optional include glob")),
		mcp.WithString("exclude_glob", mcp.Description("optional exclude glob")),
		mcp.WithString("cursor", mcp.Description("pagination cursor")),
		mcp.WithNumber("limit", mcp.Description("result limit")),
		mcp.WithNumber("max_bytes_per_file", mcp.Description("per-file read budget")),
		mcp.WithBoolean("regex", mcp.Description("interpret search query as RE2")),
		mcp.WithBoolean("case_sensitive", mcp.Description("literal search case sensitivity")),
		mcp.WithBoolean("include_sha256", mcp.Description("include matched file revisions")),
		mcp.WithBoolean("include_instructions", mcp.Description("include applicable AGENTS metadata")),
		mcp.WithNumber("context_before", mcp.Description("lines before each match")),
		mcp.WithNumber("context_after", mcp.Description("lines after each match")),
	), readOnlyToolAnnotation), r.toolContextQueryUnified)
	changeExecute := mcp.NewTool("change_execute",
		mcp.WithDescription("The sole MCPX file-modification entry: validate an apply_patch-style operation, check the current revision and policy, then atomically apply or preview it. Use after file_read/context_query; do not use it to run tests or inspect Git. Approval results include exact identifiers and must not be duplicated."), remoteSession,
		mcp.WithString("summary", mcp.Description("change summary")),
		mcp.WithArray("operations", mcp.Description("apply_patch-style file operations; required for a new change"), operationItems, mcp.MinItems(1)),
		mcp.WithString("changeset_id", mcp.Description("prepared draft Changeset to apply through this same mutation entry")),
		mcp.WithString("expected_digest", mcp.Description("digest returned when the prepared Changeset was created")),
		mcp.WithString("revert_changeset_id", mcp.Description("applied Changeset to revert through this same mutation entry")),
		mcp.WithString("idempotency_key", mcp.Description("business idempotency key for retrying the same change")),
		mcp.WithBoolean("apply", mcp.Description("apply after prepare; defaults to true")),
		mcp.WithBoolean("format", mcp.Description("format changed files")),
		mcp.WithArray("verify", mcp.Description("format, typecheck, lint, or related_tests"), mcp.WithStringItems()),
	)
	changeExecute.InputSchema = mcp.ToolInputSchema{}
	changeExecute.RawInputSchema, _ = json.Marshal(changeExecuteInputSchema())
	r.addTool(s, annotatedTool(changeExecute, mutatingToolAnnotation), r.toolChangeExecute)
	r.addTool(s, annotatedTool(mcp.NewTool("command_execute",
		mcp.WithDescription("Execute an explicit user-requested test, build, formatter, or other development command in the selected workspace. Commands are policy-checked, audited, cancellable, and may require approval; use task_manage for a running command. Do not use it to read files, inspect Git status, or apply patches."), commandRemoteSession,
		mcp.WithString("command", mcp.Description("shell command requested by the user")),
		mcp.WithString("task", mcp.Description("discovered project task name")),
		mcp.WithString("purpose", mcp.Required(), mcp.Description("why the user requested this command; do not invent a purpose")),
		mcp.WithString("scope", mcp.Enum("workspace"), mcp.Description("execution scope; defaults to workspace")),
		mcp.WithNumber("yield_time_ms", mcp.Description("wait duration; defaults to 10000, maximum 60000")),
	), commandExecutionToolAnnotation), r.toolCommandExecute)
	r.addTool(s, annotatedTool(mcp.NewTool("progress_report",
		mcp.WithDescription("Record a concise, user-visible progress update after a tool result when the agent will pause, finish, wait for the user, or otherwise not call another tool immediately. Include verified results and the next step; never put hidden chain-of-thought here."), remoteSession,
		mcp.WithString("summary", mcp.Required(), mcp.Description("verified progress summary")),
		mcp.WithString("result_summary", mcp.Description("concise result of the previous tool call")),
		mcp.WithString("status", mcp.Enum("in_progress", "completed", "waiting_for_user", "blocked"), mcp.Description("current progress state")),
		mcp.WithString("next_step", mcp.Description("next action or question for the user")),
		mcp.WithString("related_tool", mcp.Description("previous tool associated with this update")),
	), readOnlyToolAnnotation), r.toolProgressReport)

	// Domain management tools. Each action has an explicit JSON Schema branch.
	operationArraySchema := map[string]any{"type": "array", "items": changeOperationSchema(), "minItems": 1}
	r.addTool(s, actionTool("session_manage", "Manage an existing Remote Session lifecycle after session_open. Use for listing, inspecting, handoff, update, or close; do not use it to select a workspace, read files, edit files, or execute commands.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"), "workspace": stringSchema("registered workspace name"),
	}, map[string]actionSchemaBranch{
		"list":    {Properties: map[string]any{"query": stringSchema("session query"), "status": stringSchema("session status"), "cursor": stringSchema("page cursor"), "limit": numberSchema("page limit")}},
		"get":     {},
		"events":  {Properties: map[string]any{"after_sequence": numberSchema("event sequence"), "limit": numberSchema("page limit")}},
		"update":  {Properties: map[string]any{"label": stringSchema("new label"), "description": stringSchema("new description"), "status": stringSchema("session status"), "expected_version": numberSchema("optimistic-lock version")}},
		"handoff": {Properties: map[string]any{"role": stringSchema("handoff role"), "expires_in": numberSchema("handoff seconds"), "note": stringSchema("handoff note")}, Required: []string{"role"}},
		"attach":  {Properties: map[string]any{"handoff_token": stringSchema("one-time handoff token")}, Required: []string{"handoff_token"}},
		"close":   {Properties: map[string]any{"mode": stringSchema("closed or archived")}},
	}), r.toolSessionManage)
	r.addTool(s, actionToolWithAnnotation("change_manage", "Prepare or inspect a Changeset without directly changing workspace files. Use diff and history for review; use change_execute for every apply or revert, and never use command_execute for a patch.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"),
	}, map[string]actionSchemaBranch{
		"prepare": {Properties: map[string]any{"summary": stringSchema("change summary"), "operations": operationArraySchema}, Required: []string{"operations"}},
		"diff":    {Properties: map[string]any{"changeset_id": stringSchema("Changeset identifier")}, Required: []string{"changeset_id"}},
		"history": {Properties: map[string]any{"limit": numberSchema("history limit")}},
	}, sessionToolAnnotation), r.toolChangeManage)
	r.addTool(s, actionTool("task_manage", "Manage running command and project Tasks after command_execute. Use attach, status, logs, stop, or stdin with the exact remote_session_id and task_id; do not use it to start a new command or edit files. Missing Task IDs should trigger list rather than retrying the old ID.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"),
	}, map[string]actionSchemaBranch{
		"attach":      {Properties: map[string]any{"task_id": stringSchema("Task identifier"), "stdout_offset": numberSchema("absolute stdout byte offset"), "stderr_offset": numberSchema("absolute stderr byte offset"), "yield_time_ms": numberSchema("attach wait duration")}, Required: []string{"task_id"}},
		"status":      {Properties: map[string]any{"task_id": stringSchema("Task identifier")}, Required: []string{"task_id"}},
		"logs":        {Properties: map[string]any{"task_id": stringSchema("Task identifier"), "stdout_offset": numberSchema("absolute stdout byte offset"), "stderr_offset": numberSchema("absolute stderr byte offset")}, Required: []string{"task_id"}},
		"list":        {Properties: map[string]any{"limit": numberSchema("task limit")}},
		"stop":        {Properties: map[string]any{"task_id": stringSchema("Task identifier"), "force": booleanSchema("force task termination")}, Required: []string{"task_id"}},
		"ports":       {Properties: map[string]any{"task_id": stringSchema("Task identifier")}, Required: []string{"task_id"}},
		"diagnostics": {Properties: map[string]any{"task_id": stringSchema("Task identifier"), "limit": numberSchema("diagnostic limit")}, Required: []string{"task_id"}},
		"stdin":       {Properties: map[string]any{"task_id": stringSchema("Task identifier"), "input": stringSchema("stdin text")}, Required: []string{"task_id", "input"}},
	}), r.toolTaskManage)
	r.addTool(s, actionTool("plan_manage", "Create and advance a persisted development Plan. Use only for plan state and evidence; it does not modify files or run Tasks, so use change_execute and command_execute for execution.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"),
	}, map[string]actionSchemaBranch{
		"create":        {Properties: map[string]any{"goal": stringSchema("plan goal"), "summary": stringSchema("plan summary"), "tasks": arraySchema(planTaskInputSchema(), "ordered plan tasks")}, Required: []string{"goal", "tasks"}},
		"get":           {Properties: map[string]any{"plan_id": stringSchema("Plan identifier")}, Required: []string{"plan_id"}},
		"start_task":    {Properties: map[string]any{"plan_id": stringSchema("Plan identifier"), "task_id": stringSchema("Plan Task identifier")}, Required: []string{"plan_id", "task_id"}},
		"complete_task": {Properties: map[string]any{"plan_id": stringSchema("Plan identifier"), "task_id": stringSchema("Plan Task identifier"), "evidence": arraySchema(planEvidenceSchema(), "completion evidence")}, Required: []string{"plan_id", "task_id", "evidence"}},
		"block_task":    {Properties: map[string]any{"plan_id": stringSchema("Plan identifier"), "task_id": stringSchema("Plan Task identifier"), "reason": stringSchema("blocking reason"), "evidence": arraySchema(planEvidenceSchema(), "blocking evidence")}, Required: []string{"plan_id", "task_id", "reason"}},
		"replan":        {Properties: map[string]any{"plan_id": stringSchema("Plan identifier"), "goal": stringSchema("updated plan goal"), "summary": stringSchema("updated plan summary"), "reason": stringSchema("reason for replanning"), "operations": arraySchema(planOperationSchema(), "plan task operations")}, Required: []string{"plan_id", "reason", "operations"}},
		"deliver":       {Properties: map[string]any{"plan_id": stringSchema("Plan identifier")}, Required: []string{"plan_id"}},
	}), r.toolPlanManage)
	r.addTool(s, annotatedTool(actionTool("runtime_inspect", "Inspect MCPX capabilities, project summary, or scoped AGENTS instructions. Use before choosing tools or when a revision changes; it does not execute commands or modify files.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"), "workspace": stringSchema("registered workspace name"),
	}, map[string]actionSchemaBranch{
		"capabilities": {Properties: map[string]any{"include_tool_schemas": booleanSchema("include full registered tool schemas"), "include_skill_details": booleanSchema("include full skill metadata"), "known_revisions": map[string]any{"type": "object", "description": "client's previously observed capability revisions", "additionalProperties": map[string]any{"type": "string"}}}},
		"project":      {},
		"instructions": {Properties: map[string]any{"anchor_path": stringSchema("workspace-relative instruction anchor"), "paths": arraySchema(map[string]any{"type": "string"}, "multi-path instruction resolution")}},
	}), readOnlyToolAnnotation), r.toolRuntimeInspect)
	r.addTool(s, annotatedTool(mcp.NewTool("environment_inspect", mcp.WithDescription("Inspect the selected workspace's environment and toolchain without exposing secret values or modifying the environment. Use to diagnose runtime prerequisites, not to run commands."),
		remoteSession, workspace, mcp.WithArray("sections", mcp.Description("environment sections"), mcp.WithStringEnumItems(environment.ValidSections)), mcp.WithString("compare_to", mcp.Description("environment snapshot")), mcp.WithBoolean("save_snapshot", mcp.Description("persist snapshot")),
	), readOnlyToolAnnotation), r.toolEnvironmentInspect)
	r.addTool(s, actionTool("workspace_state", "Read Git status, diffs, snapshots, and workspace changes. This is the read-only Git inspection route; do not use command_execute for status or diff, and do not use it to modify files.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"),
	}, map[string]actionSchemaBranch{
		"changes":  {},
		"snapshot": {},
		"diff":     {Properties: map[string]any{"include_diff": booleanSchema("include bounded Unified Diff"), "since": stringSchema("snapshot identifier")}},
		"watch":    {Properties: map[string]any{"since": stringSchema("snapshot identifier")}},
	}), r.toolWorkspaceState)
	r.addTool(s, actionTool("extension_manage", "Discover or call configured Skills and upstream MCP extensions. Use only after session_open or workspace selection; it does not replace local file inspection, editing, or command execution.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"), "workspace": stringSchema("registered workspace name"),
	}, map[string]actionSchemaBranch{
		"list":     {Properties: map[string]any{"kind": stringSchema("skill or mcp"), "include_tools": booleanSchema("discover upstream tool schemas")}},
		"describe": {Properties: map[string]any{"kind": stringSchema("skill or mcp"), "name": stringSchema("skill name or MCP server name"), "server": stringSchema("MCP server name"), "include_tools": booleanSchema("discover upstream tool schemas")}, Required: []string{"kind"}},
		"call":     {Properties: map[string]any{"kind": stringSchema("skill or mcp"), "name": stringSchema("skill name"), "server": stringSchema("MCP server name"), "tool": stringSchema("upstream tool name"), "arguments": map[string]any{"type": "object", "additionalProperties": true}}, Required: []string{"kind"}},
	}), r.toolExtensionManage)
	r.addTool(s, actionTool("artifact_manage", "Register, list, or read Remote Session artifacts. Use for durable result files and resources; it does not modify source files or execute commands.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"),
	}, map[string]actionSchemaBranch{
		"list":     {Properties: map[string]any{"kind": stringSchema("artifact kind"), "limit": numberSchema("page limit")}},
		"read":     {Properties: map[string]any{"artifact_id": stringSchema("artifact identifier"), "offset": numberSchema("byte offset"), "limit": numberSchema("byte limit")}, Required: []string{"artifact_id"}},
		"register": {Properties: map[string]any{"path": stringSchema("workspace-relative path"), "name": stringSchema("display name"), "kind": stringSchema("artifact kind"), "mime_type": stringSchema("MIME type")}, Required: []string{"path"}},
	}), r.toolArtifactManage)
	r.addTool(s, actionToolWithAnnotation("approval_manage", "List, approve, or deny an existing security approval. Use the exact remote_session_id and approval_id returned by the requesting tool; do not guess or create a duplicate approval, and do not use this tool to edit files directly.", map[string]any{
		"remote_session_id": stringSchema("persistent Remote Session identifier"),
	}, map[string]actionSchemaBranch{
		"list":    {},
		"approve": {Properties: map[string]any{"approval_id": stringSchema("pending approval identifier")}, Required: []string{"approval_id"}},
		"deny":    {Properties: map[string]any{"approval_id": stringSchema("pending approval identifier")}, Required: []string{"approval_id"}},
	}, approvalToolAnnotation), r.toolApprovalManage)

	// Special capabilities.
	r.addTool(s, annotatedTool(mcp.NewTool("screenshot_capture", mcp.WithDescription("Capture a display or screen region for visual inspection. Use when the user requests a screenshot; it does not read source files, edit files, or execute commands."), remoteSession,
		mcp.WithString("mode", mcp.Description("fullscreen or region")), mcp.WithNumber("display", mcp.Description("display index")), mcp.WithNumber("x", mcp.Description("region X")), mcp.WithNumber("y", mcp.Description("region Y")), mcp.WithNumber("width", mcp.Description("region width")), mcp.WithNumber("height", mcp.Description("region height")), mcp.WithString("compression", mcp.Description("compression mode")), mcp.WithString("format", mcp.Description("png or jpeg")), mcp.WithNumber("quality", mcp.Description("JPEG quality")), mcp.WithNumber("max_width", mcp.Description("output width limit")), mcp.WithNumber("max_height", mcp.Description("output height limit")),
	), readOnlyToolAnnotation), r.toolScreenshotCapture)
	r.addTool(s, annotatedTool(mcp.NewTool("secrets_provide", mcp.WithDescription("Provide memory-only Secret values or references to resume an explicitly approved operation. Never place secrets in tool results or logs; this tool does not read or modify source files."), remoteSession,
		mcp.WithString("secret_id", mcp.Description("optional pending secret identifier")), mcp.WithObject("values", mcp.Description("secret name/value map; values are never persisted"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
	), secretToolAnnotation), r.toolSecretsProvide)

	r.registerResources(s)
}

func (r *Runtime) registerResources(s *mcpserver.MCPServer) {
	s.AddResourceTemplate(mcp.NewResourceTemplate("mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}", "Remote Session Artifact", mcp.WithTemplateDescription("Read a registered MCPX development artifact")), r.resourceArtifact)
	s.AddResourceTemplate(mcp.NewResourceTemplate("mcpx://remote-sessions/{remote_session_id}/changesets/{changeset_id}/diff", "Changeset Unified Diff", mcp.WithTemplateDescription("Read the complete Unified Diff for an MCPX Changeset"), mcp.WithTemplateMIMEType("text/x-diff")), r.resourceChangesetDiff)
	s.AddResourceTemplate(mcp.NewResourceTemplate("mcpx://remote-sessions/{remote_session_id}/tasks/{task_id}/logs", "Terminal Task Logs", mcp.WithTemplateDescription("Read complete logs for an MCPX terminal task"), mcp.WithTemplateMIMEType("text/plain")), r.resourceTaskLogs)
}
