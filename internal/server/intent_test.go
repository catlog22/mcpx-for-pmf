package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestRemoteRequestAllowsReadWithoutPurpose(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	var request mcp.CallToolRequest
	request.Params.Arguments = map[string]any{"workspace": "demo"}
	_, _, failure := runtime.remoteRequest(context.Background(), request)
	if failure != nil {
		t.Fatalf("read request should not require purpose: %+v", failure)
	}
}

func TestMutatingRequestRejectsOversizedPurpose(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	var request mcp.CallToolRequest
	request.Params.Arguments = map[string]any{"purpose": strings.Repeat("x", 513), "workspace": "demo"}
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
	protocol := mcpserver.NewMCPServer("mcpx-test", "1")
	runtime.registerTools(protocol)
	for name, registered := range protocol.ListTools() {
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if len(registered.Tool.RawInputSchema) > 0 {
			if err := json.Unmarshal(registered.Tool.RawInputSchema, &schema); err != nil {
				t.Errorf("tool %q schema: %v", name, err)
				continue
			}
		}
		if schema.Properties["purpose"] == nil {
			t.Errorf("tool %q does not expose purpose: %+v", name, schema.Properties)
		}
	}
}
