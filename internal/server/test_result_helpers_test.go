package server

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/arc"
)

func decodeToolResult(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result == nil {
		t.Fatal("tool returned nil result")
	}

	// 1) Model / handler structuredContent (preferred machine contract).
	if sc, ok := result.StructuredContent.(map[string]any); ok && sc != nil {
		if response, ok := normalizeStructuredResult(sc); ok {
			return response
		}
	}

	// 2) Human text may still carry legacy JSON wire payloads (pre-unify handlers).
	if len(result.Content) > 0 {
		if text, ok := result.Content[0].(*mcp.TextContent); ok {
			var response map[string]any
			if json.Unmarshal([]byte(text.Text), &response) == nil {
				if normalized, ok := normalizeStructuredResult(response); ok {
					return normalized
				}
				return response
			}
		}
	}

	// 3) Full ARC envelope in _meta.
	if value := resultARCValue(result); value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if mcpx, ok := envelope["mcpx"].(map[string]any); ok {
			if resultBody, ok := mcpx["result"].(map[string]any); ok {
				response := map[string]any{
					"status": resultBody["status"],
					"data":   resultBody["data"],
				}
				if response["status"] == nil {
					response["status"] = "succeeded"
				}
				if errBody, ok := resultBody["data"].(map[string]any); ok {
					if nestedErr, exists := errBody["error"]; exists && nestedErr != nil {
						response["error"] = nestedErr
					}
				}
				attachSessionConvenience(response)
				return response
			}
		}
		return map[string]any{"status": "succeeded", "data": envelope}
	}

	t.Fatalf("tool returned no decodable result: %+v", result)
	return nil
}

// normalizeStructuredResult accepts the unified machine shapes:
//   - model SC after ARC: {status, type, data, error?}
//   - handler wire:       {status, data, meta?, error?}
//   - bare business map:  treated as succeeded data
func normalizeStructuredResult(sc map[string]any) (map[string]any, bool) {
	if sc == nil {
		return nil, false
	}

	// Model payload from WrapToolResult.
	if sc["type"] != nil && sc["data"] != nil {
		public := coalesceStatus(sc["status"])
		response := map[string]any{
			"status":        harnessStatus(public),
			"public_status": public,
			"data":          sc["data"],
			"type":          sc["type"],
		}
		if errBody, ok := sc["error"]; ok && errBody != nil {
			response["error"] = errBody
		}
		attachSessionConvenience(response)
		return response, true
	}

	// Public wire envelope from resultJSON / compactToolResult.
	if status, ok := sc["status"].(string); ok && status != "" {
		if _, hasData := sc["data"]; hasData {
			response := map[string]any{
				"status":        harnessStatus(status),
				"public_status": status,
				"data":          sc["data"],
			}
			if errBody, ok := sc["error"]; ok && errBody != nil {
				response["error"] = errBody
			}
			if meta, ok := sc["meta"]; ok && meta != nil {
				response["meta"] = meta
			}
			attachSessionConvenience(response)
			return response, true
		}
		if errBody, ok := sc["error"]; ok && errBody != nil {
			response := map[string]any{
				"status":        harnessStatus(status),
				"public_status": status,
				"data":          sc["data"],
				"error":         errBody,
			}
			attachSessionConvenience(response)
			return response, true
		}
	}

	// Bare business payload (legacy / direct structured maps without status).
	if looksLikeBusinessPayload(sc) {
		response := map[string]any{
			"status":        "ok",
			"public_status": "succeeded",
			"data":          sc,
		}
		attachSessionConvenience(response)
		return response, true
	}
	return nil, false
}

func coalesceStatus(value any) string {
	status, _ := value.(string)
	if status == "" {
		return "succeeded"
	}
	return status
}

// harnessStatus maps public wire statuses onto the historical test harness
// vocabulary (ok) while keeping wire/canonical values available via public_status.
func harnessStatus(status string) string {
	if status == "succeeded" {
		return "ok"
	}
	return status
}

// statusOK reports success under either harness ("ok") or wire ("succeeded") spelling.
func statusOK(response map[string]any) bool {
	if response == nil {
		return false
	}
	switch response["status"] {
	case "ok", "succeeded":
		return true
	default:
		return false
	}
}

func looksLikeBusinessPayload(sc map[string]any) bool {
	if len(sc) == 0 {
		return false
	}
	// Avoid treating pure presentation maps as business data.
	if _, onlyText := sc["text"]; onlyText && len(sc) == 1 {
		return true
	}
	for key := range sc {
		switch key {
		case "status", "type", "data", "error", "meta", "hints", "actions", "summary":
			continue
		default:
			return true
		}
	}
	return false
}

func attachSessionConvenience(response map[string]any) {
	data, ok := response["data"].(map[string]any)
	if !ok || data == nil {
		return
	}
	if id, ok := data["session_id"].(string); ok && id != "" {
		response["remote_session_id"] = id
		return
	}
	if id, ok := data["remote_session_id"].(string); ok && id != "" {
		response["remote_session_id"] = id
		return
	}
	if remote, ok := data["remote_session"].(map[string]any); ok {
		if id, ok := remote["id"].(string); ok && id != "" {
			response["remote_session_id"] = id
		}
	}
}

// resultARCValue returns the full ARC envelope from _meta (not structuredContent).
func resultARCValue(result *mcp.CallToolResult) any {
	if result == nil || result.Meta == nil {
		return nil
	}
	return result.Meta[arc.ResultMetadataKey]
}

func resultMachineValue(result *mcp.CallToolResult) any {
	if result == nil {
		return nil
	}
	// Model payload first.
	if result.StructuredContent != nil {
		return result.StructuredContent
	}
	return resultARCValue(result)
}

func decodeARCEnvelope(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	value := resultARCValue(result)
	if value == nil {
		t.Fatalf("ARC result missing in _meta: %+v", result)
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

// structuredBusinessData peels unified SC to the business data map for tests
// that inspect handler fields without going through decodeToolResult.
func structuredBusinessData(result *mcp.CallToolResult) map[string]any {
	if result == nil {
		return nil
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok || sc == nil {
		return nil
	}
	if data, ok := sc["data"].(map[string]any); ok {
		return data
	}
	return sc
}

// asMapSlice normalizes JSON-decoded []any or native []map[string]any slices.
func asMapSlice(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}
