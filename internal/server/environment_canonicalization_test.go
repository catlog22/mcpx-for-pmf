package server

import (
	"encoding/json"
	"testing"

	"mcpx/internal/mcpresult"
)

func TestEnvironmentReadCanonicalizesView(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "default current", args: map[string]any{}, want: "current"},
		{name: "snapshot compare", args: map[string]any{"snapshot_id": "env_1"}, want: "compare"},
		{name: "explicit current", args: map[string]any{"view": "current", "snapshot_id": "ignored-by-current"}, want: "current"},
		{name: "explicit compare", args: map[string]any{"view": "compare", "snapshot_id": "env_1"}, want: "compare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := canonicalEnvironmentReadRequest(mcpresult.Request(tt.args))
			if got != tt.want {
				t.Fatalf("view=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestEnvironmentSchemasExposeOnlySemanticInputs(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")

	readTool := rt.toolIndex["environment_read"]
	readEncoded, err := json.Marshal(readTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var readSchema map[string]any
	if err := json.Unmarshal(readEncoded, &readSchema); err != nil {
		t.Fatal(err)
	}
	if required, _ := readSchema["required"].([]any); required != nil {
		for _, raw := range required {
			if raw == "view" {
				t.Fatalf("environment_read view must be inferable: %s", readEncoded)
			}
		}
	}

	writeTool := rt.toolIndex["environment"]
	writeEncoded, err := json.Marshal(writeTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var writeSchema map[string]any
	if err := json.Unmarshal(writeEncoded, &writeSchema); err != nil {
		t.Fatal(err)
	}
	properties := writeSchema["properties"].(map[string]any)
	if properties["action"] != nil {
		t.Fatalf("single-action environment tool must not expose action: %s", writeEncoded)
	}
}
