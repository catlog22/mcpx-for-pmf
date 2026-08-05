package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestPublicCatalogIsExactlyTheV2Contract(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	runtime.registerTools(protocol)

	want := []string{
		"workspace_list", "workspace_observe", "workspace_history_read", "session_open", "session_read", "session_transition",
		"operation_batch", "operation_manage",
		"source_read", "change_prepare", "change_read", "change_apply", "change_revert", "command_run", "task_read", "task_control",
		"progress_report", "plan_create", "plan_read", "plan_transition", "runtime_read", "environment_read", "environment_snapshot_create",
		"extension_discover", "skill_call", "mcp_call", "artifact_read", "artifact_register", "screenshot_capture", "secret_provide",
	}
	got := make([]string, 0, len(protocol.ListTools()))
	for name := range protocol.ListTools() {
		got = append(got, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public tool catalog = %v, want %v", got, want)
	}

	for name, registered := range protocol.ListTools() {
		var schema map[string]any
		if err := json.Unmarshal(registered.Tool.RawInputSchema, &schema); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s must reject unknown arguments: %s", name, registered.Tool.RawInputSchema)
		}
		properties, _ := schema["properties"].(map[string]any)
		if properties["remote_session_id"] != nil {
			t.Fatalf("%s exposes internal remote_session_id", name)
		}
		required, _ := schema["required"].([]any)
		for _, raw := range required {
			field, _ := raw.(string)
			if field == "" {
				continue
			}
			if properties[field] == nil {
				t.Fatalf("%s required field %q is missing from properties: %s", name, field, registered.Tool.RawInputSchema)
			}
		}
	}
	changeApply := protocol.ListTools()["change_apply"].Tool
	var changeSchema map[string]any
	if err := json.Unmarshal(changeApply.RawInputSchema, &changeSchema); err != nil {
		t.Fatal(err)
	}
	changeProperties, _ := changeSchema["properties"].(map[string]any)
	digestProperty, _ := changeProperties["expected_digest"].(map[string]any)
	description, _ := digestProperty["description"].(string)
	for _, phrase := range []string{"原样复制", "diff 统计", "snapshot ID"} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("change_apply expected_digest description missing %q: %s", phrase, description)
		}
	}
	operationManage := protocol.ListTools()["operation_manage"].Tool
	var operationSchema map[string]any
	if err := json.Unmarshal(operationManage.RawInputSchema, &operationSchema); err != nil {
		t.Fatal(err)
	}
	operationProperties, _ := operationSchema["properties"].(map[string]any)
	operationIDs, _ := operationProperties["operation_ids"].(map[string]any)
	if operationIDs["type"] != "array" {
		t.Fatalf("operation_manage operation_ids schema=%+v", operationIDs)
	}
	for _, raw := range operationSchema["required"].([]any) {
		if raw == "operation_id" {
			t.Fatalf("operation_manage must make operation_id conditional: %s", operationManage.RawInputSchema)
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
		t.Fatalf("operation_manage schema branches missing single=%v batch=%v: %s", sawSingle, sawBatch, operationManage.RawInputSchema)
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
