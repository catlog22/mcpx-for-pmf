package arc

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestWrapToolResultProducesARCSearchEnvelope(t *testing.T) {
	raw := mcp.NewToolResultStructured(map[string]any{
		"files":     []map[string]any{{"path": "internal/server/runtime.go"}},
		"truncated": true,
		"next_action": map[string]any{
			"tool":      "context_query",
			"arguments": map[string]any{"action": "query", "cursor": "next"},
		},
	}, "Context query returned 1 file.")
	wrapped := WrapToolResult("context_query", ResultContext{
		RequestID: "req_test", TraceID: "tr_test", SpanID: "sp_test",
		Timing: Timing{StartedAtMs: 100, ReceivedAtMs: 110, CompletedAtMs: 125,
			NetworkLatencyMs: 10, ProcessingMs: 15, ServerElapsedMs: 25},
	}, raw)

	payload := decodeEnvelope(t, wrapped)
	mcpx := payload["mcpx"].(map[string]any)
	if mcpx["version"] != Version {
		t.Fatalf("version = %v", mcpx["version"])
	}
	trace := mcpx["trace"].(map[string]any)
	if trace["request_id"] != "req_test" || trace["trace_id"] != "tr_test" || trace["span_id"] != "sp_test" {
		t.Fatalf("trace = %+v", trace)
	}
	if trace["started_at_ms"] != float64(100) || trace["received_at_ms"] != float64(110) || trace["completed_at_ms"] != float64(125) {
		t.Fatalf("trace timing = %+v", trace)
	}
	result := mcpx["result"].(map[string]any)
	if result["type"] != "search_result" || result["schema"] != SchemaSearchResult {
		t.Fatalf("result identity = %+v", result)
	}
	if result["data"].(map[string]any)["truncated"] != true {
		t.Fatalf("result data = %+v", result["data"])
	}
	if result["hints"].(map[string]any)["preferred_behavior"] != "show_directly" {
		t.Fatalf("hints = %+v", result["hints"])
	}
	actions := result["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["type"] != "continue" {
		t.Fatalf("actions = %+v", actions)
	}

	text := wrapped.Content[0].(mcp.TextContent).Text
	if text != "Context query returned 1 file." {
		t.Fatalf("non-code-change text should stay the summary, got: %q", text)
	}
}

func TestWrapToolResultMapsApprovalToErrorAction(t *testing.T) {
	raw := mcp.NewToolResultText(`{"ok":false,"status":"need_confirmation","data":{"approval_id":"ap_123","next_action":{"tool":"approval_manage","arguments":{"action":"approve","approval_id":"ap_123"}}},"error":{"code":"APPROVAL_REQUIRED","message":"approval required"}}`)
	wrapped := WrapToolResult("command_execute", ResultContext{
		RequestID: "req_approval", TraceID: "tr_approval", SpanID: "sp_approval",
		Timing: Timing{ServerElapsedMs: 4},
	}, raw)
	result := decodeEnvelope(t, wrapped)["mcpx"].(map[string]any)["result"].(map[string]any)
	if result["type"] != "error" || result["schema"] != SchemaError {
		t.Fatalf("result = %+v", result)
	}
	if result["hints"].(map[string]any)["preferred_behavior"] != "ask_confirm" {
		t.Fatalf("hints = %+v", result["hints"])
	}
	actions := result["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["type"] != "approval" {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestWrapToolResultPreservesNonTextContent(t *testing.T) {
	resource := mcp.NewResourceLink("mcpx://test/resource", "resource", "test resource", "text/plain")
	raw := mcp.NewToolResultText("plain")
	raw.Content = append(raw.Content, resource)
	wrapped := WrapToolResult("artifact_manage", ResultContext{
		RequestID: "req_resource", TraceID: "tr_resource", SpanID: "sp_resource",
	}, raw)
	if len(wrapped.Content) != 2 {
		t.Fatalf("content length = %d", len(wrapped.Content))
	}
	if _, ok := wrapped.Content[0].(mcp.TextContent); !ok {
		t.Fatalf("first content = %T", wrapped.Content[0])
	}
	if _, ok := wrapped.Content[1].(mcp.ResourceLink); !ok {
		t.Fatalf("second content = %T", wrapped.Content[1])
	}
}

func TestOutputSchemaAndRegistry(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$id"] != "mcpx.envelope.v1" {
		t.Fatalf("schema id = %v", schema["$id"])
	}
	registry := SchemaRegistry()
	if len(registry) != 13 {
		t.Fatalf("registry size = %d", len(registry))
	}
	for name, raw := range registry {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("schema %s: %v", name, err)
		}
		if item["$id"] != name {
			t.Fatalf("schema %s id = %v", name, item["$id"])
		}
	}
	registry[SchemaText][0] = 'x'
	if string(SchemaRegistry()[SchemaText]) == string(registry[SchemaText]) {
		t.Fatal("schema registry must return independent copies")
	}
	output := OutputSchema()
	output[0] = 'x'
	if OutputSchema()[0] == 'x' {
		t.Fatal("output schema must return an independent copy")
	}
}

func TestWrapToolResultAddsPresentationAndDiagramResult(t *testing.T) {
	raw := mcp.NewToolResultStructured(map[string]any{
		"text": "```mermaid\nflowchart TD\n  A --> B\n```",
	}, "diagram")
	wrapped := WrapToolResult("context_query", ResultContext{}, raw)
	result := decodeEnvelope(t, wrapped)["mcpx"].(map[string]any)["result"].(map[string]any)
	if result["type"] != "diagram" || result["schema"] != SchemaDiagram {
		t.Fatalf("result identity = %+v", result)
	}
	presentation := result["presentation"].(map[string]any)
	if presentation["default"] != "diagram" {
		t.Fatalf("presentation = %+v", presentation)
	}
}

func TestWrapToolResultKeepsTruncatedMermaidAsSearchResult(t *testing.T) {
	raw := mcp.NewToolResultStructured(map[string]any{
		"text":      "```mermaid\nflowchart TD\n  A --> B\n```",
		"truncated": true,
	}, "truncated diagram")
	wrapped := WrapToolResult("context_query", ResultContext{}, raw)
	result := decodeEnvelope(t, wrapped)["mcpx"].(map[string]any)["result"].(map[string]any)
	if result["type"] != "search_result" || result["schema"] != SchemaSearchResult {
		t.Fatalf("truncated result identity = %+v", result)
	}
}

func TestWrapToolResultUsesDiagramCollectionForMultipleCompleteBlocks(t *testing.T) {
	raw := mcp.NewToolResultStructured(map[string]any{
		"markdown": "```mermaid\nflowchart TD\n  A --> B\n```\n\n```mermaid\ngraph LR\n  C --> D\n```",
	}, "diagrams")
	wrapped := WrapToolResult("context_query", ResultContext{}, raw)
	result := decodeEnvelope(t, wrapped)["mcpx"].(map[string]any)["result"].(map[string]any)
	if result["type"] != "diagram_collection" || result["schema"] != SchemaDiagramCollection {
		t.Fatalf("collection identity = %+v", result)
	}
	data := result["data"].(map[string]any)
	if diagrams, ok := data["diagrams"].([]any); !ok || len(diagrams) != 2 {
		t.Fatalf("collection data = %+v", data)
	}
}

func TestWrapToolResultUsesStableToolSemantics(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		data     map[string]any
		wantType string
		wantView string
	}{
		{name: "context query", tool: "context_query", data: map[string]any{"content": "plain source"}, wantType: "search_result", wantView: "table"},
		{name: "change execute", tool: "change_execute", data: map[string]any{"status": "prepared"}, wantType: "code_change", wantView: "diff"},
		{name: "change manage", tool: "change_manage", data: map[string]any{"status": "applied"}, wantType: "code_change", wantView: "diff"},
		{name: "task manage", tool: "task_manage", data: map[string]any{"status": "running"}, wantType: "log", wantView: "log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := WrapToolResult(tt.tool, ResultContext{}, mcp.NewToolResultStructured(tt.data, "result"))
			result := decodeEnvelope(t, wrapped)["mcpx"].(map[string]any)["result"].(map[string]any)
			if result["type"] != tt.wantType || result["schema"] != schemaForType(tt.wantType) {
				t.Fatalf("result = %+v", result)
			}
			if result["summary"] != "result" {
				t.Fatalf("summary = %v", result["summary"])
			}
			presentation := result["presentation"].(map[string]any)
			if presentation["default"] != tt.wantView {
				t.Fatalf("presentation = %+v", presentation)
			}
		})
	}
}

func decodeEnvelope(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatal("missing structured content")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
