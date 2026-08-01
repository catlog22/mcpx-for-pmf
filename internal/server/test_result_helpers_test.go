package server

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func decodeToolResult(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if len(result.Content) > 0 {
		if text, ok := result.Content[0].(mcp.TextContent); ok {
			var response map[string]any
			if json.Unmarshal([]byte(text.Text), &response) == nil {
				return response
			}
		}
	}
	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		response := map[string]any{"status": "ok", "data": data}
		if remote, ok := data["remote_session"].(map[string]any); ok {
			if id, ok := remote["id"].(string); ok {
				response["remote_session_id"] = id
			}
		}
		return response
	}
	t.Fatalf("tool returned no decodable result: %+v", result)
	return nil
}
