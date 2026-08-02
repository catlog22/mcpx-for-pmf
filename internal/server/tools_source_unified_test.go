package server

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/arc"
)

func TestSourceReadDisplayIncludesMarkdownSourceBlock(t *testing.T) {
	data := map[string]any{
		"path":        "src/Supplier.vue",
		"content":     "<template>\n  <div />\n</template>\n",
		"offset":      4,
		"limit":       3,
		"total_lines": 10,
	}

	display := sourceReadDisplay(data, "Read src/Supplier.vue (10 lines).")
	for _, want := range []string{"Read src/Supplier.vue", "### `src/Supplier.vue` (lines 5-7 of 10)", "```vue", "<template>"} {
		if !strings.Contains(display, want) {
			t.Fatalf("source display missing %q: %s", want, display)
		}
	}
}

func TestFileReadResultExposesSourceInHostTextAndKeepsStructuredData(t *testing.T) {
	data := map[string]any{
		"path":        "src/Supplier.vue",
		"content":     "<template>\n  <div />\n</template>\n",
		"offset":      0,
		"limit":       3,
		"total_lines": 3,
	}
	raw := mcp.NewToolResultStructured(data, sourceReadDisplay(data, "Read src/Supplier.vue (3 lines)."))
	wrapped := arc.WrapToolResult("file_read", arc.ResultContext{}, raw)
	text, ok := wrapped.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("first content type = %T", wrapped.Content[0])
	}
	for _, want := range []string{"Read src/Supplier.vue", "```vue", "<template>"} {
		if !strings.Contains(text.Text, want) {
			t.Fatalf("host text missing %q: %s", want, text.Text)
		}
	}
	if _, ok := decodeARCEnvelope(t, wrapped)["mcpx"]; !ok {
		t.Fatalf("ARC metadata was dropped: %#v", wrapped.Meta)
	}
}
