package observation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestNormalizeToolInputRedactsSensitiveArguments(t *testing.T) {
	encoded, truncated := NormalizeToolInput(map[string]any{
		"intent": "inspect configuration",
		"token":  "secret-token",
		"nested": map[string]any{"password": "secret-password", "path": "config.yaml"},
	}, MaxEventBytes)
	if truncated {
		t.Fatal("small input was truncated")
	}
	text := string(encoded)
	if strings.Contains(text, "secret-token") || strings.Contains(text, "secret-password") {
		t.Fatalf("sensitive input leaked: %s", text)
	}
	if !strings.Contains(text, redactedValue) {
		t.Fatalf("redaction marker missing: %s", text)
	}
}

func TestNormalizeToolOutputOmitsBinaryContent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent("visible output"),
			mcp.ImageContent{MIMEType: "image/png", Data: "very-large-base64"},
		},
		StructuredContent: map[string]any{"api_key": "secret", "ok": true},
	}
	encoded, truncated := NormalizeToolOutput(result, MaxEventBytes)
	if truncated {
		t.Fatal("small output was truncated")
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "very-large-base64") || strings.Contains(text, "secret") {
		t.Fatalf("binary or sensitive output leaked: %s", text)
	}
	if !strings.Contains(text, "visible output") || payload["content"] == nil {
		t.Fatalf("text output missing: %s", text)
	}
}
