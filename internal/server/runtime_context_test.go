package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestRuntimeContextComesFromTransportHeaders(t *testing.T) {
	now := time.UnixMilli(1_785_486_000_100)
	headers := http.Header{
		"X-Request-ID":         []string{"req_header"},
		"Traceparent":          []string{"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"},
		"X-MCPX-Started-At-Ms": []string{"1785486000000"},
	}
	ctx, runtime := ensureRuntimeContext(context.Background(), headers, now)
	if runtime.RequestID != "req_header" || runtime.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("runtime identity = %+v", runtime)
	}
	if runtime.ParentSpanID != "0123456789abcdef" || runtime.SpanID == runtime.ParentSpanID || runtime.SpanID == "" {
		t.Fatalf("span context = %+v", runtime)
	}
	if runtime.StartedAtMs != 1785486000000 || runtime.ReceivedAtMs != now.UnixMilli() {
		t.Fatalf("runtime timing = %+v", runtime)
	}
	if got, ok := runtimeContextFrom(ctx); !ok || got.RequestID != runtime.RequestID {
		t.Fatalf("context value = %+v, %v", got, ok)
	}
}

func TestRuntimeContextGeneratesValuesWithoutHeaders(t *testing.T) {
	now := time.UnixMilli(1_785_486_000_100)
	_, runtime := ensureRuntimeContext(context.Background(), nil, now)
	if runtime.RequestID == "" || runtime.TraceID == "" || runtime.SpanID == "" {
		t.Fatalf("runtime IDs were not generated: %+v", runtime)
	}
	if runtime.StartedAtMs != now.UnixMilli() || runtime.ReceivedAtMs != now.UnixMilli() {
		t.Fatalf("generated timing = %+v", runtime)
	}
}

func TestRegisteredToolSchemasExcludeRuntimeContext(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	runtime.registerTools(protocol)
	for name, registered := range protocol.ListTools() {
		encoded, err := json.Marshal(registered.Tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		var tool map[string]any
		if err := json.Unmarshal(encoded, &tool); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		for _, field := range []string{"request_id", "trace_id", "span_id", "started_at_ms", "received_at_ms", "completed_at_ms", "processing_ms", "server_elapsed_ms"} {
			if schemaContainsProperty(tool["inputSchema"], field) {
				t.Fatalf("%s input schema contains runtime field %q: %s", name, field, encoded)
			}
		}
	}
}

func TestInstrumentToolUsesRuntimeContextWithoutArgumentMetadata(t *testing.T) {
	started := time.Now().Add(-100 * time.Millisecond).UnixMilli()
	ctx := withRuntimeContext(context.Background(), RuntimeContext{
		RequestID: "req_context", TraceID: "trace_context", SpanID: "span_context", StartedAtMs: started,
	})
	called := false
	handler := func(handlerCtx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		if _, exists := req.GetArguments()["started_at_ms"]; exists {
			t.Fatal("runtime metadata reached tool arguments")
		}
		if runtime, ok := runtimeContextFrom(handlerCtx); !ok || runtime.RequestID != "req_context" {
			t.Fatalf("handler runtime context = %+v, %v", runtime, ok)
		}
		return mcp.NewToolResultStructured(map[string]any{"value": "ok"}, "ok"), nil
	}
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{"action": "list"}
	result, err := (&Runtime{}).instrumentTool("runtime_context_test", handler)(ctx, request)
	if err != nil || !called {
		t.Fatalf("instrumented call err=%v called=%v", err, called)
	}
	if result.StructuredContent == nil {
		t.Fatal("ARC structured content missing")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	mcpx, _ := envelope["mcpx"].(map[string]any)
	trace, _ := mcpx["trace"].(map[string]any)
	if trace["request_id"] != "req_context" || trace["trace_id"] != "trace_context" || trace["span_id"] != "span_context" {
		t.Fatalf("ARC did not project runtime identity: %+v", trace)
	}
}

func schemaContainsProperty(value any, wanted string) bool {
	switch item := value.(type) {
	case map[string]any:
		if properties, ok := item["properties"].(map[string]any); ok {
			if _, exists := properties[wanted]; exists {
				return true
			}
		}
		for _, nested := range item {
			if schemaContainsProperty(nested, wanted) {
				return true
			}
		}
	case []any:
		for _, nested := range item {
			if schemaContainsProperty(nested, wanted) {
				return true
			}
		}
	}
	return false
}
