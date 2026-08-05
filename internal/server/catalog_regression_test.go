package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestReadOnlyToolAnnotationsAndSessionOpenDefaults(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	rt.registerTools(protocol)
	tools := protocol.ListTools()
	if _, exists := tools["approval_manage"]; exists {
		t.Fatal("approval_manage must not be exposed; semantic confirmation uses the original tool")
	}
	for _, name := range []string{"workspace_list", "source_read", "workspace_observe", "runtime_read", "environment_read"} {
		annotation := tools[name].Tool.Annotations
		if annotation.ReadOnlyHint == nil || !*annotation.ReadOnlyHint || annotation.DestructiveHint == nil || *annotation.DestructiveHint || annotation.IdempotentHint == nil || !*annotation.IdempotentHint {
			t.Fatalf("%s annotation is unsafe or incomplete: %+v", name, annotation)
		}
	}
	sessionAnnotation := tools["session_open"].Tool.Annotations
	if sessionAnnotation.DestructiveHint == nil || *sessionAnnotation.DestructiveHint {
		t.Fatalf("session_open should not be marked destructive: %+v", sessionAnnotation)
	}
	for _, name := range []string{"command_run"} {
		annotation := tools[name].Tool.Annotations
		if annotation.DestructiveHint == nil || *annotation.DestructiveHint {
			t.Fatalf("%s should not be marked destructive: %+v", name, annotation)
		}
	}
	var commandSchema map[string]any
	if err := json.Unmarshal(tools["command_run"].Tool.RawInputSchema, &commandSchema); err != nil {
		t.Fatal(err)
	}
	commandProperties := commandSchema["properties"].(map[string]any)
	if commandProperties["session_id"] == nil || commandProperties["purpose"] == nil || commandProperties["scope"] == nil || commandProperties["user_confirmed"] != nil {
		t.Fatalf("command_run schema must expose the public semantic fields: %+v", commandProperties)
	}
	var sessionSchema map[string]any
	if err := json.Unmarshal(tools["session_open"].Tool.RawInputSchema, &sessionSchema); err != nil {
		t.Fatal(err)
	}
	if _, exists := sessionSchema["properties"].(map[string]any)["include_skills"]; exists {
		t.Fatal("session_open must not expose request-level include_skills; use server discovery.skills config")
	}
	var sourceSchema map[string]any
	if err := json.Unmarshal(tools["source_read"].Tool.RawInputSchema, &sourceSchema); err != nil {
		t.Fatal(err)
	}
	modeSchema, ok := sourceSchema["properties"].(map[string]any)["mode"].(map[string]any)
	enumValues, _ := modeSchema["enum"].([]any)
	fullMode := false
	for _, value := range enumValues {
		if value == "full" {
			fullMode = true
			break
		}
	}
	if !ok || !fullMode {
		t.Fatalf("source_read must expose mode=full for complete client previews: %+v", modeSchema)
	}

	var request mcp.CallToolRequest
	request.Params.Arguments = map[string]any{"intent": "open the demo workspace", "workspace": "demo"}
	result, err := rt.toolSessionOpen(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("session_open structured content type=%T", result.StructuredContent)
	}
	instructions, _ := data["instructions"].(map[string]any)
	if instructions["inline"] != false {
		t.Fatalf("session_open should default to instruction metadata only: %+v", instructions)
	}
	if data["project_tasks"] != nil {
		t.Fatalf("session_open should not expand project tasks by default: %+v", data["project_tasks"])
	}
	for _, item := range data["tools"].([]map[string]any) {
		if _, exists := item["input_schema"]; exists {
			t.Fatalf("session_open should not inline full tool schemas: %+v", item)
		}
	}

	remote := data["remote_session"].(map[string]any)
	remoteID := remote["id"].(string)
	var capabilityRequest mcp.CallToolRequest
	capabilityRequest.Params.Arguments = map[string]any{"intent": "inspect capabilities", "action": "capabilities", "remote_session_id": remoteID}
	capabilityResult, err := rt.toolRuntimeInspect(context.Background(), capabilityRequest)
	if err != nil {
		t.Fatal(err)
	}
	capabilityData := capabilityResult.StructuredContent.(map[string]any)
	for _, item := range capabilityData["tool_manifest"].([]map[string]any) {
		if _, exists := item["inputSchema"]; exists {
			t.Fatalf("capabilities should default to tool summaries: %+v", item)
		}
	}

	capabilityRequest.Params.Arguments = map[string]any{"intent": "inspect capability schemas", "action": "capabilities", "remote_session_id": remoteID, "include_tool_schemas": true}
	capabilityResult, err = rt.toolRuntimeInspect(context.Background(), capabilityRequest)
	if err != nil {
		t.Fatal(err)
	}
	fullCapabilityData := capabilityResult.StructuredContent.(map[string]any)
	foundSchema := false
	for _, item := range fullCapabilityData["tool_manifest"].([]map[string]any) {
		if _, exists := item["inputSchema"]; exists {
			foundSchema = true
			break
		}
	}
	if !foundSchema {
		t.Fatal("explicit include_tool_schemas did not return registered schemas")
	}
}
