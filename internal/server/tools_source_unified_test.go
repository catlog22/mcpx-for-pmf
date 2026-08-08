package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/arc"
)

func TestSourceReadDisplayIncludesMarkdownSourceBlock(t *testing.T) {
	data := map[string]any{
		"path":        "src/Supplier.vue",
		"content":     "<template>\n  <div />\n</template>\n",
		"sha256":      "sha256:test-revision",
		"line_ending": "CRLF",
		"format": map[string]any{
			"charset": "utf-8", "bom": "none", "line_ending": "CRLF",
			"line_ending_counts": map[string]any{"lf": 0, "crlf": 2, "cr": 0},
			"final_newline":      true,
		},
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
	// format/line_ending live in structured fields only — not restated as prose.
	for _, ban := range []string{"换行：", "字符集", "末尾换行", "格式："} {
		if strings.Contains(display, ban) {
			t.Fatalf("source display must not prose-format metadata %q: %s", ban, display)
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
	raw := mcpresult.NewStructured(data, sourceReadDisplay(data, "Read src/Supplier.vue (3 lines)."))
	wrapped := arc.WrapToolResult("file_read", arc.ResultContext{}, raw)
	text, ok := wrapped.Content[0].(*mcp.TextContent)
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
		request := mcpresult.Request(map[string]any{
			"intent":            "读取客户端预览文件",
			"remote_session_id": remoteSessionID,
			"path":              path,
			"mode":              "full",
		})

		result, err := rt.toolFileReadUnified(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	htmlResult := read("preview.html")
	htmlText, ok := htmlResult.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(htmlText.Text, "```html\n"+string(html)+"```") || !strings.Contains(htmlText.Text, "Revision: `sha256:") {
		t.Fatalf("full HTML was not returned directly: %#v", htmlResult.Content)
	}
	htmlData := structuredBusinessData(htmlResult)
	if htmlData["mode"] != "full" || htmlData["mime_type"] != "text/html" || htmlData["encoding"] != "utf-8" {
		t.Fatalf("HTML metadata=%+v", htmlResult.StructuredContent)
	}
	wrappedHTML := arc.WrapToolResult("file_read", arc.ResultContext{}, htmlResult)
	wrappedHTMLText, ok := wrappedHTML.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(wrappedHTMLText.Text, string(html)) || !strings.Contains(wrappedHTMLText.Text, "</html>\n```") {
		t.Fatalf("ARC dropped full HTML content: %#v", wrappedHTML.Content)
	}

	imageResult := read("preview.png")
	if len(imageResult.Content) != 2 {
		t.Fatalf("image result content=%#v", imageResult.Content)
	}
	imageContent, ok := imageResult.Content[1].(*mcp.ImageContent)
	if !ok || imageContent.MIMEType != "image/png" {
		t.Fatalf("image content=%T %#v", imageResult.Content[1], imageResult.Content[1])
	}
	decoded := imageContent.Data
	if !bytes.Equal(decoded, imageBytes) {
		t.Fatalf("image bytes changed: size=%d/%d", len(decoded), len(imageBytes))
	}

	wrapped := arc.WrapToolResult("file_read", arc.ResultContext{}, imageResult)
	if len(wrapped.Content) != 2 {
		t.Fatalf("wrapped image content=%#v", wrapped.Content)
	}
	if _, ok := wrapped.Content[1].(*mcp.ImageContent); !ok {
		t.Fatalf("ARC dropped direct image content: %T", wrapped.Content[1])
	}
}

func TestFileReadDecodesUTF16ForModelAndWindow(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	created := callEnvelope(t, rt.toolSessionOpen, context.Background(), map[string]any{"workspace": "demo"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	content := []byte{0xff, 0xfe, 'o', 0, 'n', 0, 'e', 0, '\n', 0, 't', 0, 'w', 0, 'o', 0, '\n', 0}
	if err := os.WriteFile(registered.Path+"/utf16.txt", content, 0o644); err != nil {
		t.Fatal(err)
	}
	fullResult, err := rt.toolFileReadUnified(context.Background(), mcpresult.Request(map[string]any{
		"remote_session_id": remoteSessionID, "path": "utf16.txt", "mode": "full",
	}))
	if err != nil {
		t.Fatal(err)
	}
	full := structuredBusinessData(fullResult)
	if full["content"] != "one\ntwo\n" || full["encoding"] != "utf-8" {
		t.Fatalf("UTF-16 full result=%+v", full)
	}
	format, _ := full["format"].(map[string]any)
	if format["charset"] != "utf-16le" || format["bom"] != "utf-16le" {
		t.Fatalf("UTF-16 format=%+v", format)
	}
	windowResult, err := rt.toolFileReadUnified(context.Background(), mcpresult.Request(map[string]any{
		"remote_session_id": remoteSessionID, "path": "utf16.txt", "mode": "window", "offset": 1, "limit": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	window := structuredBusinessData(windowResult)
	if window["content"] != "two\n" || window["sha256"] != full["sha256"] {
		t.Fatalf("UTF-16 window result=%+v full=%+v", window, full)
	}
}

func TestSourceReadWindowAndBatchExposeSameFormat(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	created := callEnvelope(t, rt.toolSessionOpen, context.Background(), map[string]any{"workspace": "demo"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	content := []byte("one\r\ntwo\r\n")
	if err := os.WriteFile(registered.Path+"/format.txt", content, 0o644); err != nil {
		t.Fatal(err)
	}

	read := func(arguments map[string]any) map[string]any {
		t.Helper()
		request := mcpresult.Request(map[string]any{})
		request = mcpresult.Request(arguments)
		result, err := rt.toolFileReadUnified(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		data := structuredBusinessData(result)
		if data == nil {
			t.Fatalf("structured data=%T", result.StructuredContent)
		}
		return data
	}
	base := map[string]any{"remote_session_id": remoteSessionID, "path": "format.txt", "mode": "window", "limit": 10}
	window := read(base)
	windowFormat, ok := window["format"].(map[string]any)
	if !ok {
		t.Fatalf("window format=%T %+v", window["format"], window)
	}
	if windowFormat["line_ending"] != "CRLF" || windowFormat["charset"] != "utf-8" {
		t.Fatalf("window format=%+v", windowFormat)
	}

	batch := read(map[string]any{
		"remote_session_id": remoteSessionID,
		"mode":              "window",
		"items":             []any{map[string]any{"path": "format.txt", "limit": 10}},
	})
	results := asMapSlice(batch["results"])
	if len(results) != 1 {
		t.Fatalf("batch results=%T %+v", batch["results"], batch)
	}
	if !reflect.DeepEqual(windowFormat, results[0]["format"]) {
		t.Fatalf("window/batch format mismatch: window=%+v batch=%+v", windowFormat, results[0]["format"])
	}

	full := read(map[string]any{"remote_session_id": remoteSessionID, "path": "format.txt", "mode": "full"})
	if !reflect.DeepEqual(windowFormat, full["format"]) {
		t.Fatalf("window/full format mismatch: window=%+v full=%+v", windowFormat, full["format"])
	}
}
