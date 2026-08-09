package server

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

func TestInstrumentToolPublishesARCPresentationAndPreservesAttachments(t *testing.T) {
	resource := mcpresult.NewResourceLink("mcpx://test/artifact", "artifact", "artifact", "text/plain")
	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := mcpresult.NewStructured(map[string]any{
			"edit_id":      "edit_test",
			"status":       "succeeded",
			"diff_summary": "--- a/demo.go\n+++ b/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
			"results":      []any{map[string]any{"path": "demo.go", "operation": "update"}},
		}, "Edit applied.")
		result.Content = append(result.Content, resource)
		return result, nil
	}

	wrapped, err := (&Runtime{}).instrumentTool("edit", handler)(context.Background(), mcpresult.Request(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(wrapped.Content) != 2 {
		t.Fatalf("content length = %d", len(wrapped.Content))
	}
	if _, ok := wrapped.Content[1].(*mcp.ResourceLink); !ok {
		t.Fatalf("attachment type = %T", wrapped.Content[1])
	}

	text, ok := wrapped.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content type = %T", wrapped.Content[0])
	}
	if !strings.Contains(text.Text, "### Edit edit_test") {
		t.Fatalf("code change text must be the rendered display, got: %q", text.Text)
	}
	envelope := decodeARCEnvelope(t, wrapped)
	mcpx, _ := envelope["mcpx"].(map[string]any)
	if mcpx["version"] != "1.3" {
		t.Fatalf("ARC version = %v", mcpx["version"])
	}
	result, _ := mcpx["result"].(map[string]any)
	if result["type"] != "code_change" || result["schema"] != "mcpx.code_change.v1" {
		t.Fatalf("result = %+v", result)
	}
	presentation, _ := result["presentation"].(map[string]any)
	if presentation["default"] != "diff" {
		t.Fatalf("presentation = %+v", presentation)
	}
}
