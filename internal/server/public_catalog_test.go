package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestPublicCatalogIsExactlyTheV2Contract(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	runtime.registerTools(protocol)

	want := []string{
		"workspace_list", "workspace_observe", "workspace_history_read", "session_open", "session_read", "session_transition",
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
	}
}
