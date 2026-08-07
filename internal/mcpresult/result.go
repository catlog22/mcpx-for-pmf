// Package mcpresult provides thin helpers over the official MCP Go SDK so
// MCPX can build tool results and read arguments without mark3labs/mcp-go.
package mcpresult

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewText builds a successful tool result with human-visible text only.
func NewText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// NewStructured builds content text plus structuredContent for models.
func NewStructured(data any, text string) *mcp.CallToolResult {
	if text == "" {
		text = "succeeded"
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: data,
	}
}

// NewError builds an isError tool result with a short human message.
func NewError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
		IsError: true,
	}
}

// NewImage builds image content from raw bytes (JSON-marshaled as base64).
func NewImage(raw []byte, mimeType string) mcp.Content {
	return &mcp.ImageContent{Data: raw, MIMEType: mimeType}
}

// NewImageBase64 builds image content from a base64 string payload.
func NewImageBase64(b64, mimeType string) mcp.Content {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		raw = []byte(b64)
	}
	return NewImage(raw, mimeType)
}

// NewResourceLink builds a resource_link content block.
func NewResourceLink(uri, name, description, mimeType string) *mcp.ResourceLink {
	return &mcp.ResourceLink{
		URI:         uri,
		Name:        name,
		Description: description,
		MIMEType:    mimeType,
	}
}

// Arguments returns tool arguments as map[string]any.
func Arguments(req *mcp.CallToolRequest) map[string]any {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// Header returns HTTP headers from the transport when present.
func Header(req *mcp.CallToolRequest) http.Header {
	if req == nil || req.Extra == nil {
		return nil
	}
	return req.Extra.Header
}

// Request builds a CallToolRequest for tests/direct handler calls.
func Request(arguments map[string]any) *mcp.CallToolRequest {
	if arguments == nil {
		arguments = map[string]any{}
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		raw = []byte(`{}`)
	}
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: raw},
	}
}

// MetaGet reads a key from result _meta.
func MetaGet(result *mcp.CallToolResult, key string) any {
	if result == nil || result.Meta == nil {
		return nil
	}
	return result.Meta[key]
}

// MetaSet writes a key into result _meta.
func MetaSet(result *mcp.CallToolResult, key string, value any) {
	if result == nil {
		return
	}
	if result.Meta == nil {
		result.Meta = mcp.Meta{}
	}
	result.Meta[key] = value
}

// FirstText returns the first text content body.
func FirstText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, c := range result.Content {
		if typed, ok := c.(*mcp.TextContent); ok && typed != nil {
			return typed.Text
		}
	}
	return ""
}

// ToolSchemaJSON returns the input schema as JSON object bytes for hashing/tests.
func ToolSchemaJSON(tool mcp.Tool) json.RawMessage {
	if tool.InputSchema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	switch s := tool.InputSchema.(type) {
	case json.RawMessage:
		return s
	case []byte:
		return json.RawMessage(s)
	default:
		raw, err := json.Marshal(s)
		if err != nil {
			return json.RawMessage(`{"type":"object"}`)
		}
		return raw
	}
}
