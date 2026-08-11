package server

import (
	"encoding/json"
	"testing"

	"mcpx/internal/mcpresult"
	"mcpx/internal/skill"
)

func TestDiscoverCanonicalizesOnlyUniqueSemantics(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		wantKind string
		wantView string
	}{
		{name: "skill list", args: map[string]any{"kind": "skill"}, wantKind: "skill", wantView: "list"},
		{name: "skill describe", args: map[string]any{"kind": "skill", "name": "review"}, wantKind: "skill", wantView: "describe"},
		{name: "mcp server", args: map[string]any{"server": "dbx"}, wantKind: "mcp", wantView: "describe"},
		{name: "mcp include tools", args: map[string]any{"include_tools": true}, wantKind: "mcp", wantView: "describe"},
		{name: "ambiguous name", args: map[string]any{"name": "something"}, wantKind: "", wantView: "describe"},
		{name: "ambiguous list", args: map[string]any{}, wantKind: "", wantView: "list"},
		{name: "explicit mcp list", args: map[string]any{"kind": "mcp", "view": "list"}, wantKind: "mcp", wantView: "list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, kind, view := canonicalDiscoverRequest(mcpresult.Request(tt.args))
			if kind != tt.wantKind || view != tt.wantView {
				t.Fatalf("kind/view=%q/%q want=%q/%q", kind, view, tt.wantKind, tt.wantView)
			}
		})
	}
}

func TestSkillInventoryUsesExplicitInvocationTemplate(t *testing.T) {
	items := skillItems([]skill.Skill{{Manifest: skill.Manifest{
		Name: "review", Description: "review code", Runtime: "markdown", Format: "skill_md",
	}, Dir: "/tmp/review", Source: "/tmp"}})
	if len(items) != 1 {
		t.Fatalf("skill items=%+v", items)
	}
	item := items[0]
	if item["invocation"] != nil {
		t.Fatalf("skill inventory must not expose incomplete executable invocation: %+v", item)
	}
	template, _ := item["invocation_template"].(map[string]any)
	if template["tool"] != "skill_call" || template["requires_discovery"] != true {
		t.Fatalf("skill invocation template=%+v", template)
	}
	arguments, _ := template["arguments"].(map[string]any)
	if arguments["discovery_required"] != nil {
		t.Fatalf("template arguments must only contain public skill_call fields: %+v", arguments)
	}
	required, _ := template["required_client_fields"].([]string)
	for _, field := range []string{"remote_session_id", "purpose", "discovery_id", "discovery_revision"} {
		found := false
		for _, value := range required {
			if value == field {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("template missing required client field %q: %+v", field, template)
		}
	}
}

func TestDiscoverSchemaDoesNotRequireInferableDiscriminators(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	tool := rt.toolIndex["discover"]
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	required, _ := schema["required"].([]any)
	if !containsSchemaRequired(required, "remote_session_id") || containsSchemaRequired(required, "kind") || containsSchemaRequired(required, "view") {
		t.Fatalf("discover discriminators must be Runtime-inferable where possible: %s", encoded)
	}
}
