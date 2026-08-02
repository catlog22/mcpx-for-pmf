package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestRemoteRequestRejectsMissingIntent(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	var request mcp.CallToolRequest
	request.Params.Arguments = map[string]any{"workspace": "demo"}
	_, _, failure := runtime.remoteRequest(context.Background(), request)
	if failure == nil {
		t.Fatal("missing intent was accepted")
	}
	response := decodeToolResult(t, failure)
	if errorCode(response) != "intent_required" {
		t.Fatalf("response=%+v", response)
	}
}

func TestRemoteRequestRejectsOversizedIntent(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	var request mcp.CallToolRequest
	request.Params.Arguments = map[string]any{"intent": strings.Repeat("x", 513), "workspace": "demo"}
	_, _, failure := runtime.remoteRequest(context.Background(), request)
	if failure == nil {
		t.Fatal("oversized intent was accepted")
	}
	response := decodeToolResult(t, failure)
	if errorCode(response) != "intent_too_long" {
		t.Fatalf("response=%+v", response)
	}
}

func TestEveryRegisteredToolRequiresIntent(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	protocol := mcpserver.NewMCPServer("mcpx-test", "1")
	runtime.registerTools(protocol)
	for name, registered := range protocol.ListTools() {
		required := registered.Tool.InputSchema.Required
		if len(registered.Tool.RawInputSchema) > 0 {
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(registered.Tool.RawInputSchema, &schema); err != nil {
				t.Errorf("tool %q schema: %v", name, err)
				continue
			}
			required = schema.Required
		}
		if !containsString(required, "intent") {
			t.Errorf("tool %q does not require intent: %+v", name, required)
		}
	}
}
