package observation

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderTextShowsIntentAndStructuredToolOutput(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Tool:   "change_execute",
		Type:   TypeToolStarted,
		Intent: "update the login flow",
		Input:  []byte(`{"path":"auth.go"}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "INTENT: update the login flow") || !strings.Contains(output.String(), "```json") {
		t.Fatalf("tool start rendering=%q", output.String())
	}
}

func TestRenderTextShowsMarkdownFileDiff(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Type:    TypeFileChanged,
		Summary: "update login flow",
		Output:  []byte(`{"files":[{"path":"auth.go","operation":"update","diff":"--- a/auth.go\n+++ b/auth.go\n@@\n-old\n+new\n"}],"diff":{"resource_uri":"mcpx://changeset"}}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "```diff") || !strings.Contains(text, "-old") || !strings.Contains(text, "+new") || !strings.Contains(text, "mcpx://changeset") {
		t.Fatalf("file diff rendering=%q", text)
	}
}

func TestRenderJSONEmitsOneEventLine(t *testing.T) {
	var output bytes.Buffer
	if err := RenderJSON(&output, Event{Sequence: 7, Workspace: "demo", Type: TypeObserverNotice}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "\n") || !strings.Contains(output.String(), `"sequence":7`) {
		t.Fatalf("json rendering=%q", output.String())
	}
}
