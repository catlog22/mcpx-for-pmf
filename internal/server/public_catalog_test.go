package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/mcpresult"

	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestPublicCatalogIsExactlyTheCleanCoreContract(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)

	want := []string{
		"session", "read", "edit", "observe",
		"operation_batch", "operation_manage",
		"execute", "plan", "artifact", "discover", "skill_call", "mcp_call",
		"runtime_read", "environment_read", "environment", "screenshot_capture", "secret_provide",
	}
	got := make([]string, 0, len(runtime.listedToolMap()))
	for name := range runtime.listedToolMap() {
		got = append(got, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public tool catalog = %v, want %v", got, want)
	}

	for name, registered := range runtime.listedToolMap() {
		var schema map[string]any
		if err := json.Unmarshal(mcpresult.ToolSchemaJSON(registered), &schema); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s must reject unknown arguments: %s", name, mcpresult.ToolSchemaJSON(registered))
		}
		properties, _ := schema["properties"].(map[string]any)
		if _, clean := map[string]bool{"session": true, "read": true, "edit": true, "observe": true}[name]; clean && properties["remote_session_id"] == nil {
			t.Fatalf("%s must expose remote_session_id", name)
		}
		required, _ := schema["required"].([]any)
		for _, raw := range required {
			field, _ := raw.(string)
			if field == "" {
				continue
			}
			if properties[field] == nil {
				t.Fatalf("%s required field %q is missing from properties: %s", name, field, mcpresult.ToolSchemaJSON(registered))
			}
		}
	}
	editTool := runtime.listedToolMap()["edit"]
	var editSchema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(editTool), &editSchema); err != nil {
		t.Fatal(err)
	}
	editProperties, _ := editSchema["properties"].(map[string]any)
	if editProperties["remote_session_id"] == nil || editProperties["purpose"] == nil || editProperties["edits"] == nil {
		t.Fatalf("edit schema missing clean-core fields: %s", mcpresult.ToolSchemaJSON(editTool))
	}
	editItems, _ := editProperties["edits"].(map[string]any)
	itemSchema, _ := editItems["items"].(map[string]any)
	itemProperties, _ := itemSchema["properties"].(map[string]any)
	for _, field := range []string{"operation", "path", "base_sha256", "content", "new_path", "replacements"} {
		if itemProperties[field] == nil {
			t.Fatalf("edit item missing %q: %s", field, mcpresult.ToolSchemaJSON(editTool))
		}
	}
	operationManage := runtime.listedToolMap()["operation_manage"]
	var operationSchema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(operationManage), &operationSchema); err != nil {
		t.Fatal(err)
	}
	operationProperties, _ := operationSchema["properties"].(map[string]any)
	operationIDs, _ := operationProperties["operation_ids"].(map[string]any)
	if operationIDs["type"] != "array" {
		t.Fatalf("operation_manage operation_ids schema=%+v", operationIDs)
	}
	for _, raw := range operationSchema["required"].([]any) {
		if raw == "operation_id" {
			t.Fatalf("operation_manage must make operation_id conditional: %s", mcpresult.ToolSchemaJSON(operationManage))
		}
	}
	branches, ok := operationSchema["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("operation_manage oneOf=%T %+v", operationSchema["oneOf"], operationSchema["oneOf"])
	}
	var sawSingle, sawBatch bool
	for _, raw := range branches {
		branch := raw.(map[string]any)
		required := branch["required"].([]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)
		switch {
		case containsSchemaRequired(required, "operation_id"):
			sawSingle = true
		case containsSchemaRequired(required, "operation_ids"):
			sawBatch = true
			enum, _ := action["enum"].([]any)
			if !reflect.DeepEqual(enum, []any{"status", "result"}) && !reflect.DeepEqual(enum, []any{"result", "status"}) {
				t.Fatalf("batch actions=%v", enum)
			}
		}
	}
	if !sawSingle || !sawBatch {
		t.Fatalf("operation_manage schema branches missing single=%v batch=%v: %s", sawSingle, sawBatch, mcpresult.ToolSchemaJSON(operationManage))
	}
}

func containsSchemaRequired(required []any, want string) bool {
	for _, item := range required {
		if item == want {
			return true
		}
	}
	return false
}
