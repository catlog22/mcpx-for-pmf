package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

func TestRemoteRequestAllowsReadWithoutPurpose(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	request := mcpresult.Request(map[string]any{"workspace": "demo"})

	_, _, failure := runtime.remoteRequest(context.Background(), request)
	if failure != nil {
		t.Fatalf("read request should not require purpose: %+v", failure)
	}
}

func TestMutatingRequestRejectsOversizedPurpose(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	request := mcpresult.Request(map[string]any{"purpose": strings.Repeat("x", 513), "workspace": "demo"})

	_, _, _, failure := runtime.changeRequest(context.Background(), request, true)
	if failure == nil {
		t.Fatal("oversized purpose was accepted")
	}
	response := decodeToolResult(t, failure)
	if errorCode(response) != "purpose_required" {
		t.Fatalf("response=%+v", response)
	}
}

func TestEveryRegisteredToolExposesSemanticPurpose(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	for name, registered := range runtime.listedToolMap() {
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if len(mcpresult.ToolSchemaJSON(registered)) > 0 {
			if err := json.Unmarshal(mcpresult.ToolSchemaJSON(registered), &schema); err != nil {
				t.Errorf("tool %q schema: %v", name, err)
				continue
			}
		}
		for _, field := range []string{"goal", "purpose", "reasoning_summary", "progress_summary", "next_step", "plan_id", "plan_task_id", "execution_task_id"} {
			if schema.Properties[field] == nil {
				t.Errorf("tool %q does not expose %s: %+v", name, field, schema.Properties)
			}
		}
		if schema.Properties["task_id"] != nil {
			t.Errorf("tool %q must not expose ambiguous task_id: %+v", name, schema.Properties)
		}
	}
}

func TestPurposeSchemaGuidesSafetyBoundariesAndAuthorization(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)

	checks := map[string][]string{
		"read":     {"只读", "不修改 Workspace"},
		"execute":  {"按命令真实副作用", "pytest", "npm/pnpm/yarn test", "cargo test", "mvn/gradle test", "dotnet test", "依赖", "用户已授权", "禁止"},
		"edit":     {"用户已明确要求", "edit 不执行删除", "禁止虚构授权"},
		"move_out": {"仅冻结/预览", "用户已授权", "confirmation_uuid", "禁止虚构授权"},
	}
	registered := runtime.listedToolMap()
	for name, wanted := range checks {
		tool, ok := registered[name]
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(mcpresult.ToolSchemaJSON(tool), &schema); err != nil {
			t.Fatalf("tool %q schema: %v", name, err)
		}
		description, _ := schema.Properties["purpose"]["description"].(string)
		for _, fragment := range wanted {
			if !strings.Contains(description, fragment) {
				t.Errorf("tool %q purpose description missing %q: %q", name, fragment, description)
			}
		}
	}
}
