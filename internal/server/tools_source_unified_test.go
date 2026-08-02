package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/arc"
)

func TestSourceReadDisplayIncludesMarkdownSourceBlock(t *testing.T) {
	data := map[string]any{
		"path":        "src/Supplier.vue",
		"content":     "<template>\n  <div />\n</template>\n",
		"sha256":      "sha256:test-revision",
		"offset":      4,
		"limit":       3,
		"total_lines": 10,
	}

	display := sourceReadDisplay(data, "Read src/Supplier.vue (10 lines).")
	for _, want := range []string{"Read src/Supplier.vue", "Revision: `sha256:test-revision`", "### `src/Supplier.vue` (lines 5-7 of 10)", "```vue", "<template>"} {
		if !strings.Contains(display, want) {
			t.Fatalf("source display missing %q: %s", want, display)
		}
	}
}

func TestFileReadResultExposesSourceInHostTextAndKeepsStructuredData(t *testing.T) {
	data := map[string]any{
		"path":        "src/Supplier.vue",
		"content":     "<template>\n  <div />\n</template>\n",
		"sha256":      "sha256:test-revision",
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
	for _, want := range []string{"Read src/Supplier.vue", "Revision: `sha256:test-revision`", "```vue", "<template>"} {
		if !strings.Contains(text.Text, want) {
			t.Fatalf("host text missing %q: %s", want, text.Text)
		}
	}
	if _, ok := decodeARCEnvelope(t, wrapped)["mcpx"]; !ok {
		t.Fatalf("ARC metadata was dropped: %#v", wrapped.Meta)
	}
}

func TestSourceReadDisplayIncludesRevisionForEmptyFile(t *testing.T) {
	display := sourceReadDisplay(map[string]any{
		"path":   "empty.txt",
		"sha256": "sha256:empty-file",
	}, "Read empty.txt (0 lines).")
	if !strings.Contains(display, "Revision: `sha256:empty-file`") {
		t.Fatalf("empty-file revision missing: %s", display)
	}
}

func TestFileReadFullReturnsHTMLAndDirectImageContent(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	created := callEnvelope(t, rt.toolSessionOpen, context.Background(), map[string]any{"workspace": "demo"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	if remoteSessionID == "" {
		t.Fatalf("session=%+v", created)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}

	html := []byte("<!doctype html>\n<html>\n<body>\n" + strings.Repeat("  <p>Preview content</p>\n", 24) + "</body>\n</html>\n")
	if err := os.WriteFile(registered.Path+"/preview.html", html, 0o644); err != nil {
		t.Fatal(err)
	}
	var imageBuffer bytes.Buffer
	preview := image.NewRGBA(image.Rect(0, 0, 2, 2))
	preview.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&imageBuffer, preview); err != nil {
		t.Fatal(err)
	}
	imageBytes := imageBuffer.Bytes()
	if err := os.WriteFile(registered.Path+"/preview.png", imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	read := func(path string) *mcp.CallToolResult {
		t.Helper()
		request := mcp.CallToolRequest{}
		request.Params.Arguments = map[string]any{
			"intent":            "读取客户端预览文件",
			"remote_session_id": remoteSessionID,
			"path":              path,
			"mode":              "full",
		}
		result, err := rt.toolFileReadUnified(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	htmlResult := read("preview.html")
	htmlText, ok := htmlResult.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(htmlText.Text, "```html\n"+string(html)+"```") || !strings.Contains(htmlText.Text, "Revision: `sha256:") {
		t.Fatalf("full HTML was not returned directly: %#v", htmlResult.Content)
	}
	htmlData, ok := htmlResult.StructuredContent.(map[string]any)
	if !ok || htmlData["mode"] != "full" || htmlData["mime_type"] != "text/html" || htmlData["encoding"] != "utf-8" {
		t.Fatalf("HTML metadata=%+v", htmlResult.StructuredContent)
	}
	wrappedHTML := arc.WrapToolResult("file_read", arc.ResultContext{}, htmlResult)
	wrappedHTMLText, ok := wrappedHTML.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(wrappedHTMLText.Text, string(html)) || !strings.Contains(wrappedHTMLText.Text, "</html>\n```") {
		t.Fatalf("ARC dropped full HTML content: %#v", wrappedHTML.Content)
	}

	imageResult := read("preview.png")
	if len(imageResult.Content) != 2 {
		t.Fatalf("image result content=%#v", imageResult.Content)
	}
	imageContent, ok := imageResult.Content[1].(mcp.ImageContent)
	if !ok || imageContent.MIMEType != "image/png" {
		t.Fatalf("image content=%T %#v", imageResult.Content[1], imageResult.Content[1])
	}
	decoded, err := base64.StdEncoding.DecodeString(imageContent.Data)
	if err != nil || !bytes.Equal(decoded, imageBytes) {
		t.Fatalf("image bytes changed: err=%v size=%d/%d", err, len(decoded), len(imageBytes))
	}

	wrapped := arc.WrapToolResult("file_read", arc.ResultContext{}, imageResult)
	if len(wrapped.Content) != 2 {
		t.Fatalf("wrapped image content=%#v", wrapped.Content)
	}
	if _, ok := wrapped.Content[1].(mcp.ImageContent); !ok {
		t.Fatalf("ARC dropped direct image content: %T", wrapped.Content[1])
	}
}
