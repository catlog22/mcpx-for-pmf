package observation

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// NormalizeToolInput converts MCP arguments into bounded, redacted JSON.
// Intent remains in the input for faithful request inspection; it is also
// stored in Event.Intent for JSON/diagnostic consumers. Text rendering uses
// the concrete command, path, and result instead of exposing the intent label.
func NormalizeToolInput(arguments map[string]any, maxBytes int) (json.RawMessage, bool) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	clean, truncated := Sanitize(arguments, maxBytes)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage(`{"value":"[REDACTED]"}`), true
	}
	return encoded, truncated
}

// NormalizeToolOutput keeps the useful, human-readable part of an MCP result
// while deliberately omitting image/audio/blob bytes. Structured content is
// converted through JSON before sanitization so sensitive map keys are covered
// even when a tool returns a typed DTO.
func NormalizeToolOutput(result *mcp.CallToolResult, maxBytes int) (json.RawMessage, bool) {
	payload := map[string]any{}
	if result == nil {
		payload["available"] = false
		return json.RawMessage(`{"available":false}`), false
	}
	payload["available"] = true
	payload["is_error"] = result.IsError
	if result.StructuredContent != nil {
		payload["structured_content"] = jsonValue(result.StructuredContent)
	}
	content := make([]any, 0, len(result.Content))
	for _, item := range result.Content {
		content = append(content, normalizeContent(item))
	}
	if len(content) > 0 {
		payload["content"] = content
	}
	clean, truncated := Sanitize(payload, maxBytes)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage(`{"available":true,"truncated":true}`), true
	}
	return encoded, truncated
}

func normalizeContent(content mcp.Content) map[string]any {
	switch value := content.(type) {
	case mcp.TextContent:
		return map[string]any{"type": "text", "text": value.Text}
	case *mcp.TextContent:
		if value != nil {
			return map[string]any{"type": "text", "text": value.Text}
		}
	case mcp.ImageContent:
		return map[string]any{"type": "image", "mime_type": value.MIMEType, "bytes": len(value.Data)}
	case *mcp.ImageContent:
		if value != nil {
			return map[string]any{"type": "image", "mime_type": value.MIMEType, "bytes": len(value.Data)}
		}
	case mcp.AudioContent:
		return map[string]any{"type": "audio", "mime_type": value.MIMEType, "bytes": len(value.Data)}
	case *mcp.AudioContent:
		if value != nil {
			return map[string]any{"type": "audio", "mime_type": value.MIMEType, "bytes": len(value.Data)}
		}
	case mcp.ResourceLink:
		return map[string]any{"type": "resource_link", "uri": value.URI, "name": value.Name, "mime_type": value.MIMEType}
	case *mcp.ResourceLink:
		if value != nil {
			return map[string]any{"type": "resource_link", "uri": value.URI, "name": value.Name, "mime_type": value.MIMEType}
		}
	case mcp.EmbeddedResource:
		return normalizeEmbeddedResource(value)
	case *mcp.EmbeddedResource:
		if value != nil {
			return normalizeEmbeddedResource(*value)
		}
	default:
		return map[string]any{"type": fmt.Sprintf("%T", content)}
	}
	return map[string]any{"type": "unknown"}
}

func normalizeEmbeddedResource(value mcp.EmbeddedResource) map[string]any {
	result := map[string]any{"type": "resource"}
	switch resource := value.Resource.(type) {
	case mcp.TextResourceContents:
		result["uri"] = resource.URI
		result["mime_type"] = resource.MIMEType
		result["text"] = resource.Text
	case mcp.BlobResourceContents:
		result["uri"] = resource.URI
		result["mime_type"] = resource.MIMEType
		result["bytes"] = len(resource.Blob)
	default:
		result["resource_type"] = fmt.Sprintf("%T", resource)
	}
	return result
}

func jsonValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[UNAVAILABLE]"
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return "[UNAVAILABLE]"
	}
	return decoded
}
