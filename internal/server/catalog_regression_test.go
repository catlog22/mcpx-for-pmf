package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestReadOnlyToolAnnotationsAndSessionOpenDefaults(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	rt.registerTools(protocol)
	tools := protocol.ListTools()
	for _, name := range []string{"workspace_list", "file_read", "context_query", "runtime_inspect", "environment_inspect"} {
		annotation := tools[name].Tool.Annotations
		if annotation.ReadOnlyHint == nil || !*annotation.ReadOnlyHint || annotation.DestructiveHint == nil || *annotation.DestructiveHint || annotation.IdempotentHint == nil || !*annotation.IdempotentHint {
			t.Fatalf("%s annotation is unsafe or incomplete: %+v", name, annotation)
		}
	}
	sessionAnnotation := tools["session_open"].Tool.Annotations
	if sessionAnnotation.DestructiveHint == nil || *sessionAnnotation.DestructiveHint {
		t.Fatalf("session_open should not be marked destructive: %+v", sessionAnnotation)
	}
	for _, name := range []string{"command_execute", "approval_manage"} {
		annotation := tools[name].Tool.Annotations
		if annotation.DestructiveHint == nil || *annotation.DestructiveHint {
			t.Fatalf("%s should not be marked destructive: %+v", name, annotation)
		}
	}
	commandSchema := tools["command_execute"].Tool.InputSchema
	if !containsString(commandSchema.Required, "remote_session_id") || !containsString(commandSchema.Required, "purpose") || containsString(commandSchema.Required, "started_at_ms") || containsString(commandSchema.Required, "request_id") {
		t.Fatalf("command_execute schema must require business fields only: %+v", commandSchema.Required)
	}
	if commandSchema.Properties["purpose"] == nil || commandSchema.Properties["scope"] == nil {
		t.Fatalf("command_execute schema must expose purpose and scope: %+v", commandSchema.Properties)
	}

	var request mcp.CallToolRequest
	request.Params.Arguments = map[string]any{"workspace": "demo"}
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
	capabilityRequest.Params.Arguments = map[string]any{"action": "capabilities", "remote_session_id": remoteID}
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

	capabilityRequest.Params.Arguments = map[string]any{"action": "capabilities", "remote_session_id": remoteID, "include_tool_schemas": true}
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
