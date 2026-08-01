package arc

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestWrapToolResultExposesRecoveryActionFromErrorDetails(t *testing.T) {
	raw := mcp.NewToolResultStructured(map[string]any{
		"ok":     false,
		"status": "error",
		"error": map[string]any{
			"code":      "WORKSPACE_NOT_FOUND",
			"message":   "workspace not found",
			"category":  "not_found",
			"retryable": false,
			"details": map[string]any{
				"next_action": map[string]any{
					"tool":      "workspace_list",
					"reason":    "select a valid workspace",
					"arguments": map[string]any{},
				},
			},
		},
	}, "workspace not found")
	wrapped := WrapToolResult("session_open", ResultContext{}, raw)
	envelope := decodeEnvelope(t, wrapped)
	result := envelope["mcpx"].(map[string]any)["result"].(map[string]any)
	actions := result["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["id"] != "workspace_list" {
		t.Fatalf("recovery actions = %+v", actions)
	}
}
