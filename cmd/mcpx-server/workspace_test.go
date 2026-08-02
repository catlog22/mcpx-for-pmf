package main

import (
	"bytes"
	"strings"
	"testing"

	"mcpx/internal/observation"
)

func TestParseWorkspaceObserverArgs(t *testing.T) {
	options, err := parseWorkspaceObserverArgs([]string{"-history", "999", "-format", "JSON", "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Workspace != "demo" || options.History != observation.MaxHistory || options.Format != "json" {
		t.Fatalf("options=%+v", options)
	}
	if _, err := parseWorkspaceObserverArgs(nil); err == nil {
		t.Fatal("missing workspace should fail")
	}
	if _, err := parseWorkspaceObserverArgs([]string{"-history", "0", "demo"}); err == nil {
		t.Fatal("non-positive history should fail")
	}
	if _, err := parseWorkspaceObserverArgs([]string{"-format", "yaml", "demo"}); err == nil {
		t.Fatal("unsupported format should fail")
	}
}

func TestRenderWorkspaceFrameTextAndJSON(t *testing.T) {
	var text bytes.Buffer
	if err := renderWorkspaceFrame(&text, observation.Frame{Type: "hello", Workspace: "demo", ObserverID: "obs_1"}, "text", false); err != nil {
		t.Fatal(err)
	}
	if text.Len() != 0 {
		t.Fatalf("hello status should be hidden: %q", text.String())
	}
	text.Reset()
	if err := renderWorkspaceFrame(&text, observation.Frame{Type: "event", Event: &observation.Event{Sequence: 3, Type: observation.TypeObserverNotice}}, "json", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), `"sequence":3`) || !strings.HasSuffix(text.String(), "\n") {
		t.Fatalf("event=%q", text.String())
	}
}

func TestRenderWorkspaceFrameReusesTextRendererAcrossEvents(t *testing.T) {
	var output bytes.Buffer
	renderer := observation.NewTextRenderer(false)
	for _, frame := range []observation.Frame{
		{Type: "event", Event: &observation.Event{Sequence: 1, RequestID: "req_workspace", Tool: "file_read", Type: observation.TypeToolStarted}},
		{Type: "event", Event: &observation.Event{Sequence: 2, RequestID: "req_workspace", Tool: "file_read", Type: observation.TypeToolCompleted, Input: []byte(`{"path":"main.go"}`), Output: []byte(`{"status":"ok"}`)}},
	} {
		if err := renderWorkspaceFrameWithRenderer(&output, frame, "text", false, renderer); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(output.String(), "╭─") != 1 || strings.Count(output.String(), "╰") != 1 {
		t.Fatalf("frames did not share one interaction block: %q", output.String())
	}

	if err := renderWorkspaceFrameWithRenderer(&output, observation.Frame{Type: "gap"}, "text", false, renderer); err != nil {
		t.Fatal(err)
	}
	if err := renderWorkspaceFrameWithRenderer(&output, observation.Frame{Type: "event", Event: &observation.Event{Sequence: 3, RequestID: "req_workspace", Tool: "file_read", Type: observation.TypeToolCompleted, Output: []byte(`{"status":"ok"}`)}}, "text", false, renderer); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "╭─") != 2 {
		t.Fatalf("gap did not reset interaction state: %q", output.String())
	}
}
