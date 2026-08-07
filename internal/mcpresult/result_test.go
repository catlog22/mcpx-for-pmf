package mcpresult

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewImageAppend(t *testing.T) {
	r := NewStructured(map[string]any{"a": 1}, "hi")
	img := NewImage([]byte{1, 2, 3, 4}, "image/jpeg")
	if img == nil {
		t.Fatal("nil image")
	}
	r.Content = append(r.Content, img)
	if len(r.Content) != 2 {
		t.Fatalf("len=%d", len(r.Content))
	}
	if _, ok := r.Content[1].(*mcp.ImageContent); !ok {
		t.Fatalf("type %T", r.Content[1])
	}
}
