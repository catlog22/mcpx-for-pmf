package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mcpx/internal/mcpresult"
)

func TestRuntimeReadCanonicalizesView(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "default capabilities", args: map[string]any{}, want: "capabilities"},
		{name: "anchor instructions", args: map[string]any{"anchor_path": "internal/server/runtime.go"}, want: "instructions"},
		{name: "paths instructions", args: map[string]any{"paths": []any{"AGENTS.md"}}, want: "instructions"},
		{name: "explicit project", args: map[string]any{"view": "project"}, want: "project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := canonicalRuntimeReadRequest(mcpresult.Request(tt.args))
			if got != tt.want {
				t.Fatalf("view=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestRuntimeReadAndProgressSchemasMatchSimplifiedProtocol(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")

	runtimeTool := rt.toolIndex["runtime_read"]
	runtimeEncoded, err := json.Marshal(runtimeTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeSchema map[string]any
	if err := json.Unmarshal(runtimeEncoded, &runtimeSchema); err != nil {
		t.Fatal(err)
	}
	if required, _ := runtimeSchema["required"].([]any); required != nil {
		for _, raw := range required {
			if raw == "view" {
				t.Fatalf("runtime_read view must be inferable: %s", runtimeEncoded)
			}
		}
	}

	progressTool := rt.toolIndex["progress"]
	progressEncoded, err := json.Marshal(progressTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var progressSchema map[string]any
	if err := json.Unmarshal(progressEncoded, &progressSchema); err != nil {
		t.Fatal(err)
	}
	status := progressSchema["properties"].(map[string]any)["status"].(map[string]any)
	description, _ := status["description"].(string)
	if strings.Contains(description, "停止 MCPX 工具调用前必须") || !strings.Contains(description, "不要求额外 progress") {
		t.Fatalf("progress status still encodes client bookkeeping: %q", description)
	}
}
