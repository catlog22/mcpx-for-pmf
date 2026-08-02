package server

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/arc"
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
	if value := resultMachineValue(result); value != nil {
		raw, err := json.Marshal(value)
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

func resultMachineValue(result *mcp.CallToolResult) any {
	if result == nil {
		return nil
	}
	if result.StructuredContent != nil {
		return result.StructuredContent
	}
	if result.Meta != nil {
		return result.Meta.AdditionalFields[arc.ResultMetadataKey]
	}
	return nil
}

func decodeARCEnvelope(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	value := resultMachineValue(result)
	if value == nil {
		t.Fatalf("ARC result missing: %+v", result)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}
