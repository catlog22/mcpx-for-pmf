package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/auth"
	"mcpx/internal/config"
)

// TestA01A02A03A07A10A13ViaMCPProtocol exercises the real Streamable HTTP path:
// client → tools/list / call_tool → MCPX handlers (acceptance A01/A02/A03/A07/A10/A13 core).
func TestA01A02A03A07A10A13ViaMCPProtocol(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(workspace, "frontend", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"demo.go":                     "package demo\n\nconst Value = 1\n",
		"a.go":                        "package demo\n\nfunc Alpha() int { return 1 }\n",
		"b.go":                        "package demo\n\nfunc Beta() int { return 2 }\n",
		"delete_me.txt":               "remove through confirmation\n",
		"AGENTS.md":                   "# Project: chinese comments\n",
		"frontend/AGENTS.md":          "# frontend: use pnpm\n",
		"frontend/src/AGENTS.md":      "# src: no generated\n",
		"go.mod":                      "module demo\n\ngo 1.22\n",
		"frontend/src/views/Home.vue": "<template><div/></template>\n",
	}
	for rel, content := range files {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	globalAgents := filepath.Join(home, "GLOBAL_AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("# Global: run all tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	token := "acceptance-token"
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = token
	cfg.Logging.Enabled = false
	cfg.Security.Commands.Allow = append(cfg.Security.Commands.Allow, `^printf\b`, `^sleep\b`, `^go test\b`)
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspace}}
	cfg.Discovery.Instructions.GlobalAgentsPath = globalAgents
	// Keep this protocol fixture independent of the developer machine's global
	// ~/.agents/skills and ~/.codex/skills directories.
	cfg.Discovery.Skills.Dirs = []string{filepath.Join(home, "skills")}
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{Version: "0.9.0-test", Commit: "deadbeef", Date: "2026-07-31"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	protocol := mcpserver.NewMCPServer("mcpx", "0.9.0-test",
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithResourceCapabilities(true, false),
	)
	runtime.registerTools(protocol)
	streamable := mcpserver.NewStreamableHTTPServer(protocol,
		mcpserver.WithDisableLocalhostProtection(true),
		mcpserver.WithHTTPContextFunc(func(ctx context.Context, req *http.Request) context.Context {
			return auth.ContextWithAuthorization(ctx, req.Header.Get("Authorization"))
		}),
	)
	gw := NewGateway(cfg, nil, streamable)
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	client, err := mcpclient.NewStreamableHttpClient(ts.URL+"/mcp", mcptransport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + token,
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start client: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "acceptance-client", Version: "1.0.0"}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// --- A01: tools/list schema ---
	listed, err := client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	byName := map[string]mcp.Tool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	expectedTools := []string{
		"workspace_list", "workspace_observe", "workspace_history_read", "session_open", "session_read", "session_transition",
		"operation_batch", "operation_manage",
		"source_read", "change_prepare", "change_read", "change_apply", "change_revert", "command_run", "task_read", "task_control",
		"progress_report", "plan_create", "plan_read", "plan_transition", "runtime_read", "environment_read", "environment_snapshot_create",
		"extension_discover", "skill_call", "mcp_call", "artifact_read", "artifact_register", "screenshot_capture", "secret_provide",
	}
	if len(byName) != len(expectedTools) {
		t.Fatalf("tools/list count=%d, want %d: %v", len(byName), len(expectedTools), byName)
	}
	for _, required := range expectedTools {
		if _, ok := byName[required]; !ok {
			t.Fatalf("tools/list missing %s", required)
		}
	}
	for name, tool := range byName {
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		var listedTool map[string]any
		if err := json.Unmarshal(encoded, &listedTool); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		outputSchema, ok := listedTool["outputSchema"].(map[string]any)
		properties, propertiesOK := outputSchema["properties"].(map[string]any)
		if !ok || !propertiesOK || properties["mcpx"] == nil {
			t.Fatalf("%s missing ARC outputSchema: %+v", name, listedTool["outputSchema"])
		}
		inputSchema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema %s: %v", name, err)
		}
		for _, forbidden := range []string{"presentation", "renderer", "show_source", "density"} {
			if strings.Contains(string(inputSchema), `"`+forbidden+`"`) {
				t.Fatalf("%s exposes host presentation argument %q: %s", name, forbidden, inputSchema)
			}
		}
	}
	prepareTool := byName["change_prepare"]
	schemaJSON, err := json.Marshal(prepareTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schemaText := string(schemaJSON)
	for _, needle := range []string{"base_sha256", "patch", "content", "operations"} {
		if !strings.Contains(schemaText, needle) {
			t.Fatalf("change_prepare schema missing %q: %s", needle, schemaText)
		}
	}
	// operations enum should advertise create/update/rename/delete (and exact ops).
	if !strings.Contains(schemaText, "update") || !strings.Contains(schemaText, "create") {
		t.Fatalf("change_prepare operations enum incomplete: %s", schemaText)
	}
	if strings.Contains(schemaText, "user_confirmed") {
		t.Fatalf("change_prepare exposes removed compatibility field user_confirmed: %s", schemaText)
	}
	commandSchema, _ := json.Marshal(byName["command_run"].InputSchema)
	for _, required := range []string{"session_id", "purpose"} {
		if !strings.Contains(string(commandSchema), `"`+required+`"`) {
			t.Fatalf("command_run schema missing %q: %s", required, commandSchema)
		}
	}
	if !strings.Contains(string(commandSchema), "scope") {
		t.Fatalf("command_run schema missing scope: %s", commandSchema)
	}
	contextSchema, _ := json.Marshal(byName["source_read"].InputSchema)
	for _, removed := range []string{"pattern", "max_files"} {
		if strings.Contains(string(contextSchema), removed) {
			t.Fatalf("source_read exposes removed compatibility field %q: %s", removed, contextSchema)
		}
	}
	if !strings.Contains(string(contextSchema), `"purpose"`) {
		t.Fatalf("source_read schema must expose purpose: %s", contextSchema)
	}
	extensionSchema, _ := json.Marshal(byName["extension_discover"].InputSchema)
	if !strings.Contains(string(extensionSchema), "view") || !strings.Contains(string(extensionSchema), "include_tools") || !strings.Contains(string(extensionSchema), "server") {
		t.Fatalf("extension_discover schema incomplete: %s", extensionSchema)
	}
	planSchema, _ := json.Marshal(byName["plan_create"].InputSchema)
	for _, forbidden := range []string{"presentation", "renderer", "show_source", "density"} {
		if strings.Contains(string(planSchema), forbidden) {
			t.Fatalf("plan_manage exposes host presentation field %q: %s", forbidden, planSchema)
		}
	}

	// Catalog names must match tools/list (A01).
	declared := capabilityToolNames()
	if len(declared) != len(listed.Tools) {
		t.Fatalf("catalog count %d != tools/list %d", len(declared), len(listed.Tools))
	}

	rawCall := func(name string, args map[string]any) map[string]any {
		t.Helper()
		if _, exists := args["purpose"]; !exists {
			withPurpose := make(map[string]any, len(args)+1)
			for key, value := range args {
				withPurpose[key] = value
			}
			withPurpose["purpose"] = "acceptance operation"
			args = withPurpose
		}
		var req mcp.CallToolRequest
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := client.CallTool(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(res.Content) == 0 {
			t.Fatalf("%s empty content", name)
		}
		text, ok := res.Content[0].(mcp.TextContent)
		if !ok {
			// The public result is human-first. Keep this fallback for attached
			// content and older clients that still return a machine payload.
			if value := resultMachineValue(res); value != nil {
				raw, _ := json.Marshal(value)
				var asMap map[string]any
				if json.Unmarshal(raw, &asMap) == nil {
					// Normalize to envelope-like shape for callers that expect status.
					if _, hasOK := asMap["ok"]; !hasOK {
						return map[string]any{"ok": true, "status": "succeeded", "data": asMap, "_raw_structured": true, "_result": res}
					}
					return asMap
				}
			}
			t.Fatalf("%s content type %T", name, res.Content[0])
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
			// The first text content is the host-visible display; the ARC
			// envelope is kept in response metadata for machine consumers.
			if value := resultMachineValue(res); value != nil {
				raw, _ := json.Marshal(value)
				_ = json.Unmarshal(raw, &envelope)
			}
		}
		if mcpx, ok := envelope["mcpx"].(map[string]any); ok {
			if _, hasLegacyOK := envelope["ok"]; hasLegacyOK {
				t.Fatalf("%s returned legacy top-level Envelope alongside ARC: %s", name, text.Text)
			}
			result, _ := mcpx["result"].(map[string]any)
			publicStatus, _ := result["status"].(string)
			if publicStatus == "" {
				publicStatus = "succeeded"
			}
			status := publicStatus
			if status == "succeeded" {
				status = "ok"
			} else if status == "waiting_confirmation" {
				status = "need_confirmation"
			}
			okValue := publicStatus == "succeeded"
			hints, _ := result["hints"].(map[string]any)
			if result["type"] == "error" {
				okValue = false
			}
			if hints["preferred_behavior"] == "ask_confirm" && status == "succeeded" {
				status, okValue = "waiting_confirmation", false
			}
			normalized := map[string]any{"ok": okValue, "status": status, "public_status": publicStatus, "data": result["data"], "_arc": envelope, "_result": res, "_text": text.Text}
			if resultData, ok := result["data"].(map[string]any); ok {
				if errData, exists := resultData["error"]; exists {
					normalized["error"] = errData
				}
			}
			return normalized
		}
		if envelope == nil {
			t.Fatalf("%s decode: %v\n%s", name, err, text.Text)
		}
		envelope["_result"] = res
		envelope["_text"] = text.Text
		return envelope
	}

	copyArgs := func(input map[string]any) map[string]any {
		output := make(map[string]any, len(input)+3)
		for key, value := range input {
			output[key] = value
		}
		return output
	}
	normalizeCall := func(name string, input map[string]any) (string, map[string]any) {
		args := copyArgs(input)
		if _, exists := args["session_id"]; !exists {
			if value, exists := args["remote_session_id"]; exists {
				args["session_id"] = value
			}
		}
		delete(args, "remote_session_id")
		if _, exists := args["purpose"]; !exists {
			if value, exists := args["intent"]; exists {
				args["purpose"] = value
			}
		}
		if _, exists := args["purpose"]; !exists {
			args["purpose"] = "acceptance operation"
		}
		delete(args, "intent")
		switch name {
		case "file_read":
			name, args["view"] = "source_read", "file"
		case "context_query":
			action, _ := args["action"].(string)
			view := map[string]string{"list": "list", "query": "context", "search": "search"}[action]
			if view == "" {
				view = "search"
			}
			name, args["view"] = "source_read", view
			delete(args, "action")
		case "command_execute":
			name = "command_run"
		case "task_manage":
			action, _ := args["action"].(string)
			if action == "attach" || action == "stop" || action == "stdin" {
				name, args["operation"] = "task_control", action
			} else {
				name, args["view"] = "task_read", action
			}
			delete(args, "action")
		case "plan_manage":
			action, _ := args["action"].(string)
			switch action {
			case "create":
				name = "plan_create"
			case "get":
				name = "plan_read"
			default:
				name, args["transition"] = "plan_transition", action
			}
			delete(args, "action")
		case "runtime_inspect":
			name, args["view"] = "runtime_read", args["action"]
			delete(args, "action")
		case "change_execute":
			if revertID, exists := args["revert_changeset_id"]; exists {
				name, args["changeset_id"] = "change_revert", revertID
				delete(args, "revert_changeset_id")
				delete(args, "apply")
			} else if _, exists := args["changeset_id"]; exists {
				name = "change_apply"
				delete(args, "apply")
				delete(args, "user_confirmed")
			} else {
				name = "change_prepare"
				delete(args, "apply")
			}
		}
		return name, args
	}
	call := func(name string, input map[string]any) map[string]any {
		publicName, args := normalizeCall(name, input)
		if name == "change_execute" {
			if _, hasOperations := args["operations"]; hasOperations {
				apply, _ := input["apply"].(bool)
				prepared := rawCall("change_prepare", args)
				if !apply || prepared["ok"] != true {
					return prepared
				}
				preparedData, _ := prepared["data"].(map[string]any)
				if preparedData["idempotent_replay"] == true {
					return prepared
				}
				applyArgs := map[string]any{
					"session_id": args["session_id"], "changeset_id": preparedData["changeset_id"],
					"expected_digest": preparedData["digest"], "purpose": args["purpose"],
				}
				if value, exists := args["format"]; exists {
					applyArgs["format"] = value
				}
				if value, exists := args["verify"]; exists {
					applyArgs["verify"] = value
				}
				return rawCall("change_apply", applyArgs)
			}
		}
		return rawCall(publicName, args)
	}

	// --- A03: session_open single call ---
	opened := call("session_open", map[string]any{
		"workspace":                    "project",
		"label":                        "acceptance",
		"include_instructions_content": true,
		"include_project_tasks":        true,
		"include_upstream_tools":       false,
	})
	if opened["status"] != "ok" && opened["ok"] != true {
		t.Fatalf("session_open: %+v", opened)
	}
	openData, _ := opened["data"].(map[string]any)
	if openData == nil {
		t.Fatalf("session_open data missing: %+v", opened)
	}
	guidance, _ := openData["agent_guidance"].(map[string]any)
	routing, _ := guidance["tool_routing"].(map[string]any)
	responseContract, _ := guidance["response_contract"].(map[string]any)
	if guidance["version"] != agentGuidanceVersion || !containsAnyString(routing["modify_files"], "change_prepare") || responseContract["required"] != true {
		t.Fatalf("session_open guidance missing or incomplete: %+v", guidance)
	}
	remoteSession, _ := openData["remote_session"].(map[string]any)
	remoteID, _ := remoteSession["id"].(string)
	if remoteID == "" {
		t.Fatalf("session_open missing remote_session.id: %+v", openData)
	}
	if openData["session_id"] != remoteID {
		t.Fatalf("session_open missing top-level session_id: %+v", openData)
	}
	revs, _ := openData["revisions"].(map[string]any)
	for _, key := range []string{
		"tool_schema_revision", "capability_manifest_revision", "guidance_revision", "skill_manifest_revision",
		"mcp_manifest_revision", "instruction_revision", "session_capability_revision",
	} {
		if revs[key] == nil || revs[key] == "" {
			t.Fatalf("missing revision %s: %+v", key, revs)
		}
	}
	mcpxMeta, _ := openData["mcpx"].(map[string]any)
	if mcpxMeta["version"] != "0.9.0-test" {
		t.Fatalf("mcpx version: %+v", mcpxMeta)
	}
	instr, _ := openData["instructions"].(map[string]any)
	docs, _ := instr["documents"].([]any)
	if len(docs) < 2 {
		t.Fatalf("expected global+project instructions, got %+v", instr)
	}
	// Inline content present for at least one doc.
	hasContent := false
	for _, raw := range docs {
		doc, _ := raw.(map[string]any)
		if c, ok := doc["content"].(string); ok && c != "" {
			hasContent = true
		}
	}
	if !hasContent {
		t.Fatalf("session_open should inline AGENTS content: %+v", instr)
	}
	schemaRev1 := fmt.Sprint(revs["tool_schema_revision"])
	sessionRev1 := fmt.Sprint(revs["session_capability_revision"])
	initialRefresh, _ := openData["client_refresh"].(map[string]any)
	if initialRefresh["required"] != true || initialRefresh["tool_schema_revision"] != schemaRev1 {
		t.Fatalf("initial client refresh contract missing: %+v", initialRefresh)
	}

	// --- P0 plan_manage: create, advance, and deliver through ARC ---
	planCreated := call("plan_manage", map[string]any{
		"action": "create", "remote_session_id": remoteID, "goal": "acceptance plan",
		"tasks": []any{map[string]any{"task_id": "verify", "title": "Verify protocol"}},
	})
	planData, _ := planCreated["data"].(map[string]any)
	rawPlan, _ := planCreated["_arc"].(map[string]any)
	if rawPlan == nil {
		t.Fatalf("plan_manage response lacks ARC envelope: %+v", planCreated)
	}
	arcResult := rawPlan["mcpx"].(map[string]any)["result"].(map[string]any)
	planPresentation := arcResult["presentation"].(map[string]any)
	if planCreated["status"] != "ok" || planData["plan_id"] == nil || arcResult["type"] != "plan" || planPresentation["default"] != "task_list" {
		t.Fatalf("plan create = %+v", planCreated)
	}
	planID, _ := planData["plan_id"].(string)
	planTasks, _ := planData["tasks"].([]any)
	taskID := ""
	if len(planTasks) > 0 {
		taskID, _ = planTasks[0].(map[string]any)["task_id"].(string)
	}
	if !strings.HasPrefix(taskID, "pt_") {
		t.Fatalf("plan_create must issue server task id: %+v", planData)
	}
	started := call("plan_manage", map[string]any{"action": "start_task", "remote_session_id": remoteID, "plan_id": planID, "task_id": taskID})
	if started["status"] != "ok" || started["data"].(map[string]any)["task_id"] != taskID {
		t.Fatalf("plan start = %+v", started)
	}
	completed := call("plan_manage", map[string]any{
		"action": "complete_task", "remote_session_id": remoteID, "plan_id": planID, "task_id": taskID,
		"evidence": []any{map[string]any{"kind": "source", "reference_id": "demo.go"}},
	})
	if completed["status"] != "ok" || completed["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("plan complete = %+v", completed)
	}
	delivered := call("plan_manage", map[string]any{"action": "deliver", "remote_session_id": remoteID, "plan_id": planID})
	deliveryArc := delivered["_arc"].(map[string]any)["mcpx"].(map[string]any)["result"].(map[string]any)
	if delivered["status"] != "ok" || delivered["data"].(map[string]any)["ready"] != true || deliveryArc["type"] != "delivery" {
		t.Fatalf("plan deliver = %+v", delivered)
	}

	// --- A02: runtime_inspect capabilities revisions; role-independent tool_schema_revision ---
	caps := call("runtime_inspect", map[string]any{"action": "capabilities", "remote_session_id": remoteID})
	capData, _ := caps["data"].(map[string]any)
	capRevs, _ := capData["revisions"].(map[string]any)
	if fmt.Sprint(capRevs["tool_schema_revision"]) != schemaRev1 {
		t.Fatalf("tool_schema_revision drifted between session_open and capability_list: %v vs %v", schemaRev1, capRevs["tool_schema_revision"])
	}
	if fmt.Sprint(capRevs["session_capability_revision"]) != sessionRev1 {
		// Same session/role — should match.
		t.Fatalf("session_capability_revision mismatch: %v vs %v", sessionRev1, capRevs["session_capability_revision"])
	}
	resumed := call("session_open", map[string]any{
		"remote_session_id": remoteID,
		"known_revisions": map[string]any{
			"tool_schema_revision": schemaRev1,
			"skill_revision":       revs["skill_revision"], "mcp_revision": revs["mcp_revision"],
			"instruction_revision": revs["instruction_revision"], "session_capability_revision": sessionRev1,
		},
	})
	resumedData, _ := resumed["data"].(map[string]any)
	if omitted, _ := resumedData["omitted_sections"].([]any); len(omitted) != 4 {
		t.Fatalf("session_open should omit unchanged revision payloads: %+v", resumedData)
	}
	resumedRefresh, _ := resumedData["client_refresh"].(map[string]any)
	if resumedRefresh["required"] != false {
		t.Fatalf("session_open should not require refresh for current schema: %+v", resumedRefresh)
	}
	changedRefresh := clientRefreshPayload(map[string]any{"known_revisions": map[string]any{"tool_schema_revision": "old"}}, map[string]any{"tool_schema_revision": schemaRev1})
	if changedRefresh["required"] != true {
		t.Fatalf("changed tool schema should require refresh: %+v", changedRefresh)
	}

	// --- A04 nested AGENTS ---
	listedInstr := call("runtime_inspect", map[string]any{
		"action":            "instructions",
		"remote_session_id": remoteID,
		"anchor_path":       "frontend/src/views/Home.vue",
	})
	listedData, _ := listedInstr["data"].(map[string]any)
	chain, _ := listedData["instructions"].([]any)
	if len(chain) < 4 {
		t.Fatalf("nested AGENTS chain too short: %+v", listedData)
	}
	scopes := []string{}
	for _, raw := range chain {
		doc, _ := raw.(map[string]any)
		scopes = append(scopes, fmt.Sprint(doc["scope"]))
	}
	joined := strings.Join(scopes, ",")
	if !strings.Contains(joined, "global") || !strings.Contains(joined, "project") || !strings.Contains(joined, "directory") {
		t.Fatalf("expected global/project/directory scopes, got %v", scopes)
	}

	// --- A07 file_read_batch ---
	batch := call("file_read", map[string]any{
		"remote_session_id": remoteID,
		"items": []any{
			map[string]any{"path": "a.go", "offset": 0, "limit": 20},
			map[string]any{"path": "b.go", "offset": 0, "limit": 20},
			map[string]any{"path": "missing.go", "offset": 0, "limit": 10},
		},
	})
	batchData, _ := batch["data"].(map[string]any)
	results, _ := batchData["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("batch results: %+v", batchData)
	}
	okCount, failCount := 0, 0
	var demoSHA string
	for _, raw := range results {
		item, _ := raw.(map[string]any)
		if item["ok"] == true {
			okCount++
			if item["path"] == "a.go" {
				demoSHA, _ = item["sha256"].(string)
			}
		} else {
			failCount++
		}
	}
	if okCount != 2 || failCount != 1 {
		t.Fatalf("batch ok/fail = %d/%d data=%+v", okCount, failCount, batchData)
	}
	// Consistency with single file_read
	single := call("file_read", map[string]any{"remote_session_id": remoteID, "path": "a.go", "offset": 0, "limit": 20})
	singleData, _ := single["data"].(map[string]any)
	if singleData["sha256"] != demoSHA {
		t.Fatalf("batch sha %q != single %q", demoSHA, singleData["sha256"])
	}

	// --- A08 code_search context ---
	search := call("context_query", map[string]any{
		"action":            "search",
		"remote_session_id": remoteID,
		"query":             "Alpha",
		"context_before":    1,
		"context_after":     1,
		"include_sha256":    true,
	})
	searchData, _ := search["data"].(map[string]any)
	matches, _ := searchData["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("search missed Alpha: %+v", searchData)
	}
	match0, _ := matches[0].(map[string]any)
	if match0["sha256"] == nil || match0["sha256"] == "" {
		t.Fatalf("search missing sha256: %+v", match0)
	}
	scopedQuery := call("context_query", map[string]any{
		"action": "query", "remote_session_id": remoteID,
		"query": "检查 Alpha 实现代码", "mode": "smart", "parallel": true, "max_results": 10,
		"paths": []any{"."}, "include_glob": "**/*.go",
	})
	scopedData, _ := scopedQuery["data"].(map[string]any)
	scopedFiles, _ := scopedData["files"].([]any)
	if len(scopedFiles) == 0 {
		t.Fatalf("recursive directory query returned no source files: %+v", scopedData)
	}
	foundAlpha := false
	for _, raw := range scopedFiles {
		item, _ := raw.(map[string]any)
		if item["path"] == "a.go" {
			foundAlpha = true
		}
	}
	if !foundAlpha {
		t.Fatalf("recursive directory query missed a.go: %+v", scopedData)
	}

	// --- A08 command_execute / task_manage short and long command paths ---
	short := call("command_execute", map[string]any{
		"remote_session_id": remoteID, "command": "printf short-command", "purpose": "run the short command protocol check", "scope": "workspace",
	})
	shortData, _ := short["data"].(map[string]any)
	if shortData["completed_in_call"] != true || shortData["exit_code"] != float64(0) || shortData["task_id"] != "" {
		t.Fatalf("short command should complete in one call: %+v", short)
	}
	long := call("command_execute", map[string]any{
		"remote_session_id": remoteID, "command": "sleep 0.05", "purpose": "verify short wait task handoff", "scope": "workspace", "yield_time_ms": 1,
	})
	longData, _ := long["data"].(map[string]any)
	longTaskID, _ := longData["task_id"].(string)
	if longData["completed_in_call"] != false || longTaskID == "" {
		t.Fatalf("long command should return a unified Task: %+v", long)
	}
	attached := call("task_manage", map[string]any{
		"action": "attach", "remote_session_id": remoteID, "task_id": longTaskID, "yield_time_ms": 1000,
	})
	attachedData, _ := attached["data"].(map[string]any)
	if attachedData["status"] != "exited" || attachedData["exit_code"] != float64(0) || attachedData["stdout_next_offset"] == nil || attachedData["stderr_next_offset"] == nil {
		t.Fatalf("task attach must return stream-specific offsets: %+v", attached)
	}
	overTen := call("command_execute", map[string]any{
		"remote_session_id": remoteID, "command": "sleep 11", "purpose": "verify default long-task handoff", "scope": "workspace",
	})
	overTenData, _ := overTen["data"].(map[string]any)
	overTenTaskID, _ := overTenData["task_id"].(string)
	if overTenData["completed_in_call"] != false || overTenTaskID == "" {
		t.Fatalf("command longer than the default 10s yield should return a Task: %+v", overTen)
	}
	stopped := call("task_manage", map[string]any{
		"action": "stop", "remote_session_id": remoteID, "task_id": overTenTaskID,
	})
	if stopped["status"] != "ok" {
		t.Fatalf("long command Task could not be stopped: %+v", stopped)
	}

	// --- A10/A13 change_execute + diff summary ---
	sum := sha256.Sum256([]byte(files["demo.go"]))
	base := fmt.Sprintf("sha256:%x", sum[:])
	executed := call("change_execute", map[string]any{
		"remote_session_id": remoteID,
		"idempotency_key":   "change-idempotency-key",
		"summary":           "bump Value",
		"apply":             true,
		"format":            true,
		"verify":            []any{"related_tests"},
		"operations": []any{map[string]any{
			"operation": "replace_exact", "path": "demo.go",
			"base_sha256": base, "match": "const Value = 1", "replacement": "const Value = 2",
			"occurrence": "one",
		}},
	})
	// change_execute returns structured DTO (possibly as data wrapper)
	execData, _ := executed["data"].(map[string]any)
	if execData == nil {
		t.Fatalf("change_execute: %+v", executed)
	}
	if execData["applied"] != true {
		// may need confirmation in some policies — default file policy allows
		if executed["status"] == "need_confirmation" {
			t.Fatalf("ordinary src update should not need confirmation: %+v", executed)
		}
		t.Fatalf("change_execute not applied: %+v", execData)
	}
	if execData["changeset_id"] == "" || execData["digest"] == "" {
		t.Fatalf("missing changeset identity: %+v", execData)
	}
	diffMeta, _ := execData["diff"].(map[string]any)
	if diffMeta["mode"] != "inline" && diffMeta["mode"] != "resource" {
		t.Fatalf("diff mode: %+v", diffMeta)
	}
	if diffMeta["resource_uri"] == "" {
		t.Fatalf("missing diff resource URI: %+v", diffMeta)
	}
	if diffMeta["mode"] == "inline" {
		inlineDiff, _ := diffMeta["unified_diff"].(string)
		text := fmt.Sprint(executed["_text"])
		if inlineDiff != "" && strings.Count(text, inlineDiff) != 1 {
			t.Fatalf("ARC content must carry the inline diff exactly once: %s", text)
		}
	}
	content, err := os.ReadFile(filepath.Join(workspace, "demo.go"))
	if err != nil || !strings.Contains(string(content), "Value = 2") {
		t.Fatalf("file not updated: %q err=%v", content, err)
	}
	verifyResults, ok := execData["verify"].([]any)
	if !ok || len(verifyResults) != 1 {
		t.Fatalf("change_execute verification result missing: %+v", execData["verify"])
	}
	verifyResult, _ := verifyResults[0].(map[string]any)
	if verifyResult["status"] != "exited" || verifyResult["exit_code"] != float64(0) {
		t.Fatalf("change_execute verification failed: %+v", verifyResult)
	}
	replayed := call("change_execute", map[string]any{
		"remote_session_id": remoteID, "idempotency_key": "change-idempotency-key", "summary": "ignored on replay", "apply": true,
		"operations": []any{map[string]any{"operation": "replace_exact", "path": "demo.go", "base_sha256": base, "match": "const Value = 1", "replacement": "const Value = 2", "occurrence": "one"}},
	})
	replayData, _ := replayed["data"].(map[string]any)
	if replayData["changeset_id"] != execData["changeset_id"] || replayData["idempotent_replay"] != true {
		t.Fatalf("change_execute retry must replay its original Changeset: %+v", replayed)
	}
	deleteSum := sha256.Sum256([]byte(files["delete_me.txt"]))
	pendingDelete := call("change_execute", map[string]any{
		"remote_session_id": remoteID, "summary": "remove controlled file", "apply": true,
		"operations": []any{map[string]any{"operation": "delete", "path": "delete_me.txt", "base_sha256": fmt.Sprintf("sha256:%x", deleteSum[:])}},
	})
	if pendingDelete["status"] != "need_confirmation" {
		t.Fatalf("delete must require semantic confirmation: %+v", pendingDelete)
	}
	pendingData, _ := pendingDelete["data"].(map[string]any)
	approvedDelete := call("change_execute", map[string]any{
		"remote_session_id": remoteID, "changeset_id": pendingData["changeset_id"],
		"expected_digest": pendingData["digest"], "confirmation_token": pendingData["confirmation_token"],
	})
	if approvedDelete["status"] != "ok" {
		t.Fatalf("semantic confirmation did not resume the original Changeset: %+v", approvedDelete)
	}
	if _, err := os.Stat(filepath.Join(workspace, "delete_me.txt")); !os.IsNotExist(err) {
		t.Fatalf("approved delete did not remove target: %v", err)
	}

	// --- A12 stale revision ---
	stale := call("change_execute", map[string]any{
		"remote_session_id": remoteID,
		"summary":           "stale",
		"apply":             true,
		"operations": []any{map[string]any{
			"operation": "replace_exact", "path": "demo.go",
			"base_sha256": base, // old hash
			"match":       "const Value = 2", "replacement": "const Value = 3", "occurrence": "one",
		}},
	})
	if stale["status"] == "ok" {
		t.Fatalf("stale revision should fail: %+v", stale)
	}
	errBody, _ := stale["error"].(map[string]any)
	code, _ := errBody["code"].(string)
	if code != "STALE_REVISION" || errBody["category"] != "conflict" || errBody["retryable"] != true {
		t.Fatalf("expected retryable stale-revision contract, got %+v", stale)
	}
	// File must remain at Value = 2
	content, _ = os.ReadFile(filepath.Join(workspace, "demo.go"))
	if strings.Contains(string(content), "Value = 3") {
		t.Fatal("stale write must not apply")
	}

	// --- A11 zero match fails ---
	freshSum := sha256.Sum256(content)
	freshBase := fmt.Sprintf("sha256:%x", freshSum[:])
	nomatch := call("change_execute", map[string]any{
		"remote_session_id": remoteID,
		"summary":           "no match",
		"apply":             false,
		"operations": []any{map[string]any{
			"operation": "replace_exact", "path": "demo.go",
			"base_sha256": freshBase, "match": "DOES_NOT_EXIST_XYZ", "replacement": "x", "occurrence": "one",
		}},
	})
	if nomatch["status"] == "ok" {
		t.Fatalf("zero match should fail: %+v", nomatch)
	}

	// --- P2 MCP-only handoff: revert also uses the sole change_execute mutation entry. ---
	reverted := call("change_execute", map[string]any{
		"remote_session_id":   remoteID,
		"revert_changeset_id": execData["changeset_id"],
		"apply":               true,
	})
	if reverted["status"] != "ok" {
		t.Fatalf("change_execute revert failed: %+v", reverted)
	}
	content, err = os.ReadFile(filepath.Join(workspace, "demo.go"))
	if err != nil || !strings.Contains(string(content), "Value = 1") {
		t.Fatalf("revert did not restore file: %q err=%v", content, err)
	}
}
