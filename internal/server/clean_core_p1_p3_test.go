package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/config"
)

func TestCleanCoreExecuteIdempotencyAndUserConfirmation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands.Allow = append(rt.cfg.Security.Commands.Allow, `^printf\b`)
	rt.cfg.Security.Commands.Confirm = append(rt.cfg.Security.Commands.Confirm, `^echo\b`)
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	request := map[string]any{
		"action": "run", "remote_session_id": remoteID, "purpose": "run a stable command",
		"command": "printf stable", "scope": "workspace", "idempotency_key": "execute-replay-1",
	}
	first := callEnvelope(t, rt.toolExecute, context.Background(), request)
	if !statusOK(first) {
		t.Fatalf("execute failed: %+v", first)
	}
	firstData := first["data"].(map[string]any)
	if firstData["completed_in_call"] != true || firstData["exit_code"] != float64(0) {
		t.Fatalf("short execute result=%+v", firstData)
	}

	replayRequest := cloneMap(request)
	replayRequest["purpose"] = "same effect with a rephrased purpose"
	replay := callEnvelope(t, rt.toolExecute, context.Background(), replayRequest)
	if !statusOK(replay) || replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("execute retry did not replay=%+v", replay)
	}
	conflictRequest := cloneMap(request)
	conflictRequest["command"] = "printf changed"
	conflict := callEnvelope(t, rt.toolExecute, context.Background(), conflictRequest)
	if statusOK(conflict) || errorCode(conflict) != "idempotency_conflict" {
		t.Fatalf("execute conflict=%+v", conflict)
	}

	confirmationRequest := map[string]any{
		"action": "run", "remote_session_id": remoteID, "purpose": "run a confirmed command",
		"command": "echo confirmed", "scope": "workspace", "idempotency_key": "execute-confirm-1",
	}
	waiting := callEnvelope(t, rt.toolExecute, context.Background(), confirmationRequest)
	if waiting["status"] != "waiting_confirmation" {
		t.Fatalf("confirmation should wait=%+v", waiting)
	}
	waitingData, _ := waiting["data"].(map[string]any)
	if waitingData["user_confirmed_required"] != true || waitingData["confirmation_token"] != nil {
		t.Fatalf("clean confirmation leaked token or missed user flag=%+v", waitingData)
	}
	confirmedRequest := cloneMap(confirmationRequest)
	confirmedRequest["user_confirmed"] = true
	confirmed := callEnvelope(t, rt.toolExecute, context.Background(), confirmedRequest)
	if !statusOK(confirmed) {
		t.Fatalf("confirmed execute failed=%+v", confirmed)
	}
	confirmedReplay := callEnvelope(t, rt.toolExecute, context.Background(), confirmedRequest)
	if !statusOK(confirmedReplay) || confirmedReplay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("confirmed retry did not replay=%+v", confirmedReplay)
	}
}

func TestCleanCoreMissingCommandUsesExecutionTaxonomy(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands.Allow = append(rt.cfg.Security.Commands.Allow, `^mcpx-command-that-does-not-exist$`)
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	response := callEnvelope(t, rt.toolExecute, context.Background(), map[string]any{
		"action": "run", "remote_session_id": remoteID, "purpose": "classify a missing executable",
		"command": "mcpx-command-that-does-not-exist", "scope": "workspace",
	})
	if statusOK(response) || errorCode(response) != "command_not_found" {
		t.Fatalf("missing command response=%+v", response)
	}
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["category"] != "execution" || errorBody["retryable"] != false {
		t.Fatalf("missing command taxonomy=%+v", errorBody)
	}
	details, _ := errorBody["details"].(map[string]any)
	if details["exit_code"] != float64(127) {
		t.Fatalf("missing command exit code=%+v", details)
	}

	accepted := callEnvelope(t, rt.toolHandlers["execute"], context.Background(), map[string]any{
		"action": "run", "remote_session_id": remoteID, "purpose": "classify async command failure",
		"command": "mcpx-command-that-does-not-exist", "scope": "workspace", "execution_mode": "async",
	})
	if accepted["status"] != "accepted" {
		t.Fatalf("async missing command was not accepted as an operation: %+v", accepted)
	}
	acceptedData, _ := accepted["data"].(map[string]any)
	operationID, _ := acceptedData["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("async operation id missing: %+v", accepted)
	}
	completed := callEnvelope(t, rt.toolHandlers["operation_manage"], context.Background(), map[string]any{
		"remote_session_id": remoteID, "action": "wait", "operation_id": operationID, "timeout_ms": 5000,
	})
	if statusOK(completed) || errorCode(completed) != "operation_failed" {
		t.Fatalf("async operation failure=%+v", completed)
	}
	operationError, _ := completed["error"].(map[string]any)
	if operationError["category"] != "execution" || operationError["retryable"] != false {
		t.Fatalf("async operation taxonomy=%+v", operationError)
	}
	operationDetails, _ := operationError["details"].(map[string]any)
	if operationDetails["exit_code"] != float64(127) {
		t.Fatalf("async operation exit code=%+v", operationDetails)
	}
}

func TestCleanCorePlanEvidenceAndArtifactWorkflow(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands.Allow = append(rt.cfg.Security.Commands.Allow, `^sleep\b`)
	workspace, _ := rt.reg.Get("demo")
	path := filepath.Join(workspace.Path, "plan.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	created := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "create", "remote_session_id": remoteID, "purpose": "track the workflow",
		"goal": "complete the clean core workflow", "tasks": []any{map[string]any{"local_id": "main", "title": "apply and verify"}},
		"idempotency_key": "plan-create-1",
	})
	if !statusOK(created) {
		t.Fatalf("plan create=%+v", created)
	}
	createdData := created["data"].(map[string]any)
	planID := createdData["plan_id"].(string)
	replay := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "create", "remote_session_id": remoteID, "purpose": "same plan, rephrased",
		"goal": "complete the clean core workflow", "tasks": []any{map[string]any{"local_id": "main", "title": "apply and verify"}},
		"idempotency_key": "plan-create-1",
	})
	if replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("plan create replay=%+v", replay)
	}
	tasks := asMapSlice(createdData["tasks"])
	taskID := tasks[0]["plan_task_id"].(string)
	advanced := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "advance", "remote_session_id": remoteID, "purpose": "start the tracked task", "plan_id": planID, "plan_task_id": taskID,
	})
	if !statusOK(advanced) {
		t.Fatalf("plan advance=%+v", advanced)
	}

	base := digestForTest([]byte("before\n"))
	edited := callEnvelope(t, rt.toolEdit, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "apply the tracked edit", "idempotency_key": "plan-edit-1",
		"edits": []any{map[string]any{"path": "plan.txt", "operation": "update", "base_sha256": base,
			"replacements": []any{map[string]any{"match": "before", "replacement": "after"}}}},
	})
	if !statusOK(edited) {
		t.Fatalf("edit=%+v", edited)
	}
	editID := edited["data"].(map[string]any)["edit_id"].(string)

	executed := callEnvelope(t, rt.toolExecute, context.Background(), map[string]any{
		"action": "run", "remote_session_id": remoteID, "purpose": "run the tracked verification", "command": "sleep 0.05",
		"scope": "workspace", "yield_time_ms": 1,
	})
	if executed["status"] != "accepted" && !statusOK(executed) {
		t.Fatalf("execute=%+v", executed)
	}
	executeData := executed["data"].(map[string]any)
	taskIDForEvidence, _ := executeData["execution_task_id"].(string)
	if taskIDForEvidence == "" {
		t.Fatalf("expected a task for execution evidence=%+v", executeData)
	}
	attached := callEnvelope(t, rt.toolExecute, context.Background(), map[string]any{
		"action": "attach", "remote_session_id": remoteID, "purpose": "collect verification output", "execution_task_id": taskIDForEvidence, "yield_time_ms": 1000,
	})
	if !statusOK(attached) || attached["data"].(map[string]any)["status"] != "exited" {
		t.Fatalf("attach=%+v", attached)
	}

	artifact := callEnvelope(t, rt.toolArtifactClean, context.Background(), map[string]any{
		"action": "register", "remote_session_id": remoteID, "purpose": "record the edited artifact", "path": "plan.txt", "kind": "other", "idempotency_key": "artifact-register-1",
	})
	if !statusOK(artifact) {
		t.Fatalf("artifact register=%+v", artifact)
	}
	artifactID := artifact["data"].(map[string]any)["artifact_id"].(string)

	completed := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "complete", "remote_session_id": remoteID, "purpose": "record verifiable completion", "plan_id": planID, "plan_task_id": taskID,
		"evidence": []any{
			map[string]any{"kind": "edit", "reference_id": editID},
			map[string]any{"kind": "execute", "reference_id": taskIDForEvidence},
			map[string]any{"kind": "artifact", "reference_id": artifactID},
		},
	})
	if !statusOK(completed) || completed["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("plan complete=%+v", completed)
	}
	delivered := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "deliver", "remote_session_id": remoteID, "purpose": "deliver the completed workflow", "plan_id": planID,
	})
	deliveryData := delivered["data"].(map[string]any)
	if !statusOK(delivered) || deliveryData["ready"] != true {
		t.Fatalf("plan deliver=%+v", delivered)
	}
}

func TestCleanCoreDiscoveryIsAnExplicitExtraCall(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	rt.cfg.Discovery.Skills.Enabled = true
	rt.cfg.Discovery.Skills.Dirs = []string{".skills"}
	skillDir := filepath.Join(workspace.Path, ".skills", "docs")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: docs\ndescription: return documentation\nruntime: markdown\n---\n\n# Docs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	skipped := callEnvelope(t, rt.toolSkillCallClean, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "call the documentation skill", "name": "docs", "arguments": map[string]any{}, "idempotency_key": "skill-discovery-idem-1",
	})
	if statusOK(skipped) || errorCode(skipped) != "discovery_required" {
		t.Fatalf("skill call without discover=%+v", skipped)
	}
	if details, _ := skipped["error"].(map[string]any)["details"].(map[string]any); details["required_call_count"] != float64(1) || details["discovery_required"] != true {
		t.Fatalf("discovery cost was not exposed=%+v", skipped)
	}
	if len(rt.discoveries) != 0 {
		t.Fatalf("skill call implicitly discovered a lease: %+v", rt.discoveries)
	}

	discovered := callEnvelope(t, rt.toolDiscover, context.Background(), map[string]any{
		"remote_session_id": remoteID, "kind": "skill", "view": "describe", "name": "docs",
	})
	if !statusOK(discovered) {
		t.Fatalf("skill discover=%+v", discovered)
	}
	discoveryData := discovered["data"].(map[string]any)
	called := callEnvelope(t, rt.toolSkillCallClean, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "call the documentation skill", "name": "docs", "arguments": map[string]any{},
		"idempotency_key": "skill-discovery-idem-1",
		"discovery_id":    discoveryData["discovery_id"], "discovery_revision": discoveryData["discovery_revision"],
	})
	calledData := called["data"].(map[string]any)
	if !statusOK(called) || !strings.Contains(calledData["content"].(string), "# Docs") {
		t.Fatalf("skill call after explicit discover=%+v", called)
	}

	// A manifest revision change invalidates the old lease and makes the next
	// call explain that another explicit discover is required.
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: docs\ndescription: changed documentation\nruntime: markdown\n---\n\n# Docs v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := callEnvelope(t, rt.toolSkillCallClean, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "call the documentation skill", "name": "docs", "arguments": map[string]any{},
		"discovery_id": discoveryData["discovery_id"], "discovery_revision": discoveryData["discovery_revision"],
	})
	if statusOK(stale) || errorCode(stale) != "discovery_stale" {
		t.Fatalf("stale skill discovery=%+v", stale)
	}
}

func TestCleanCoreMCPDiscoveryRequiresExtraCall(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.MCP.Enabled = true
	workspace, _ := rt.reg.Get("demo")
	script := filepath.Join(t.TempDir(), "fake_mcp.py")
	serverCode := `#!/usr/bin/env python3
import json
import sys

def send(message):
    sys.stdout.write(json.dumps(message, separators=(',', ':')) + "\n")
    sys.stdout.flush()

tools = [{"name": "echo", "description": "echo a value", "inputSchema": {"type": "object", "properties": {"value": {"type": "string"}}, "required": ["value"]}}]
for line in sys.stdin:
    try:
        request = json.loads(line)
    except Exception:
        continue
    request_id = request.get("id")
    if request_id is None:
        continue
    method = request.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": request_id, "result": {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}}, "serverInfo": {"name": "fake", "version": "1"}}})
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": request_id, "result": {"tools": tools}})
    elif method == "tools/call":
        value = request.get("params", {}).get("arguments", {}).get("value", "")
        send({"jsonrpc": "2.0", "id": request_id, "result": {"content": [{"type": "text", "text": "echo:" + value}], "isError": False}})
    else:
        send({"jsonrpc": "2.0", "id": request_id, "error": {"code": -32601, "message": "method not found"}})
`
	if err := os.WriteFile(script, []byte(serverCode), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteMCPFile(config.ProjectMCPPath(workspace.Path), config.MCPFile{MCPServers: map[string]config.MCPServer{
		"fake": {Command: "python3", Args: []string{script}},
	}}); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	withoutDiscover := callEnvelope(t, rt.toolMCPCallClean, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "call the fake MCP", "server": "fake", "tool": "echo", "arguments": map[string]any{"value": "one"}, "idempotency_key": "mcp-discovery-idem-1",
	})
	if statusOK(withoutDiscover) || errorCode(withoutDiscover) != "discovery_required" {
		t.Fatalf("MCP call without discover=%+v", withoutDiscover)
	}
	if len(rt.discoveries) != 0 {
		t.Fatalf("MCP call implicitly discovered a lease: %+v", rt.discoveries)
	}

	discovered := callEnvelope(t, rt.toolDiscover, context.Background(), map[string]any{
		"remote_session_id": remoteID, "kind": "mcp", "view": "describe", "server": "fake", "include_tools": true,
	})
	if !statusOK(discovered) {
		t.Fatalf("MCP discover=%+v", discovered)
	}
	discoveryData := discovered["data"].(map[string]any)
	called := callEnvelope(t, rt.toolMCPCallClean, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "call the fake MCP", "server": "fake", "tool": "echo", "arguments": map[string]any{"value": "one"},
		"idempotency_key": "mcp-discovery-idem-1",
		"discovery_id":    discoveryData["discovery_id"], "discovery_revision": discoveryData["discovery_revision"],
	})
	if !statusOK(called) {
		t.Fatalf("MCP call after discover=%+v", called)
	}
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
