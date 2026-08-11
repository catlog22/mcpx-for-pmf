package server

import (
	"encoding/json"
	"testing"

	"mcpx/internal/mcpresult"
)

func TestObserveCanonicalizesUniqueTargets(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "default session", args: map[string]any{"remote_session_id": "rs_1"}, want: "session"},
		{name: "execution task", args: map[string]any{"remote_session_id": "rs_1", "execution_task_id": "task_1"}, want: "task"},
		{name: "plan task", args: map[string]any{"remote_session_id": "rs_1", "plan_task_id": "pt_1"}, want: "plan"},
		{name: "edit diff", args: map[string]any{"remote_session_id": "rs_1", "edit_id": "edit_1"}, want: "diff"},
		{name: "history keyword", args: map[string]any{"remote_session_id": "rs_1", "keyword": "panic"}, want: "history"},
		{name: "explicit logs", args: map[string]any{"remote_session_id": "rs_1", "view": "logs", "execution_task_id": "task_1"}, want: "logs"},
		{name: "conflicting targets", args: map[string]any{"remote_session_id": "rs_1", "execution_task_id": "task_1", "keyword": "panic"}, want: ""},
		{name: "ambiguous path", args: map[string]any{"remote_session_id": "rs_1", "path": "internal"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := canonicalObserveRequest(mcpresult.Request(tt.args))
			if got != tt.want {
				t.Fatalf("view=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestObservePublicSchemaMatchesCanonicalSemantics(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	tool := rt.toolIndex["observe"]
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	for _, raw := range schema["required"].([]any) {
		if raw == "view" {
			t.Fatalf("observe view should be inferable, schema=%s", encoded)
		}
	}
	properties := schema["properties"].(map[string]any)
	view := properties["view"].(map[string]any)
	values := map[string]bool{}
	for _, raw := range view["enum"].([]any) {
		values[raw.(string)] = true
	}
	if !values["task"] || values["status"] {
		t.Fatalf("observe view enum must use task instead of status: %+v", values)
	}
	if properties["stdout_offset"] == nil || properties["stderr_offset"] == nil {
		t.Fatalf("observe logs must accept server next_action offsets: %s", encoded)
	}
}
