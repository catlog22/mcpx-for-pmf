package server

import (
	"mcpx/internal/mcpresult"

	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadOnlyToolAnnotationsAndSessionOpenDefaults(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	rt.registerTools(protocol)
	tools := rt.listedToolMap()
	if _, exists := tools["approval_manage"]; exists {
		t.Fatal("approval_manage must not be exposed; semantic confirmation uses the original tool")
	}
	for _, name := range []string{"workspace_read", "source_read", "change_read", "runtime_read", "environment_read"} {
		annotation := tools[name].Annotations
		if !annotation.ReadOnlyHint || annotation.DestructiveHint != nil && *annotation.DestructiveHint || !annotation.IdempotentHint {
			t.Fatalf("%s annotation is unsafe or incomplete: %+v", name, annotation)
		}
	}
	sessionAnnotation := tools["session"].Annotations
	if sessionAnnotation.DestructiveHint != nil && *sessionAnnotation.DestructiveHint {
		t.Fatalf("session should not be marked destructive: %+v", sessionAnnotation)
	}
	for _, name := range []string{"command_run"} {
		annotation := tools[name].Annotations
		if annotation.DestructiveHint != nil && *annotation.DestructiveHint {
			t.Fatalf("%s should not be marked destructive: %+v", name, annotation)
		}
	}
	var commandSchema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(tools["command_run"]), &commandSchema); err != nil {
		t.Fatal(err)
	}
	commandProperties := commandSchema["properties"].(map[string]any)
	if commandProperties["session_id"] == nil || commandProperties["purpose"] == nil || commandProperties["scope"] == nil || commandProperties["user_confirmed"] != nil {
		t.Fatalf("command_run schema must expose the public semantic fields: %+v", commandProperties)
	}
	var sessionSchema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(tools["session"]), &sessionSchema); err != nil {
		t.Fatal(err)
	}
	if _, exists := sessionSchema["properties"].(map[string]any)["include_skills"]; exists {
		t.Fatal("session must not expose request-level include_skills; use server discovery.skills config")
	}
	var sourceSchema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(tools["source_read"]), &sourceSchema); err != nil {
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

	request := mcpresult.Request(map[string]any{"intent": "open the demo workspace", "workspace": "demo"})

	result, err := rt.toolSessionOpen(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	data := structuredBusinessData(result)
	if data == nil {
		t.Fatalf("session structured content type=%T", result.StructuredContent)
	}
	instructions, _ := data["instructions"].(map[string]any)
	if instructions["inline"] != false {
		t.Fatalf("session should default to instruction metadata only: %+v", instructions)
	}
	if data["project_tasks"] != nil {
		t.Fatalf("session should not expand project tasks by default: %+v", data["project_tasks"])
	}
	for _, item := range asMapSlice(data["tools"]) {
		if _, exists := item["input_schema"]; exists {
			t.Fatalf("session should not inline full tool schemas: %+v", item)
		}
	}

	remote := data["remote_session"].(map[string]any)
	remoteID := remote["id"].(string)
	capabilityRequest := mcpresult.Request(map[string]any{"intent": "inspect capabilities", "action": "capabilities", "remote_session_id": remoteID})

	capabilityResult, err := rt.toolRuntimeInspect(context.Background(), capabilityRequest)
	if err != nil {
		t.Fatal(err)
	}
	capabilityData := structuredBusinessData(capabilityResult)
	for _, item := range asMapSlice(capabilityData["tool_manifest"]) {
		if _, exists := item["inputSchema"]; exists {
			t.Fatalf("capabilities should default to tool summaries: %+v", item)
		}
	}

	capabilityRequest = mcpresult.Request(map[string]any{"intent": "inspect capability schemas", "action": "capabilities", "remote_session_id": remoteID, "include_tool_schemas": true})

	capabilityResult, err = rt.toolRuntimeInspect(context.Background(), capabilityRequest)
	if err != nil {
		t.Fatal(err)
	}
	fullCapabilityData := structuredBusinessData(capabilityResult)
	foundSchema := false
	for _, item := range asMapSlice(fullCapabilityData["tool_manifest"]) {
		if _, exists := item["inputSchema"]; exists {
			foundSchema = true
			break
		}
	}
	if !foundSchema {
		t.Fatal("explicit include_tool_schemas did not return registered schemas")
	}
}
