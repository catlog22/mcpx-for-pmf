package observation

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestTextRendererGroupsInteractionIntoBoundedBlock(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	events := []Event{
		{Sequence: 42, RequestID: "req_42", Tool: "command_execute", Type: TypeToolStarted, Input: []byte(`{"command":"go test ./internal/auth"}`)},
	}
	for sequence := int64(43); sequence <= 46; sequence++ {
		events = append(events, Event{
			Sequence: sequence, RequestID: "req_42", Tool: "command_execute", Type: TypeCommandOutput,
			Stream: "stdout", Output: []byte(fmt.Sprintf(`{"text":"line-%d-a\nline-%d-b","bytes":17}`, sequence, sequence)),
		})
	}
	events = append(events, Event{
		Sequence: 47, RequestID: "req_42", Tool: "command_execute", Type: TypeToolCompleted,
		Input:  []byte(`{"command":"go test ./internal/auth"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Command completed."}]}}`),
	})
	for _, event := range events {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}

	text := output.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > maxInteractionLines {
		t.Fatalf("lines=%d output=%q", len(lines), text)
	}
	if strings.Count(text, "╭─") != 1 || strings.Count(text, "╰") != 1 {
		t.Fatalf("interaction was not grouped: %q", text)
	}
	if !strings.Contains(text, "#42 · command_execute") || !strings.Contains(text, "...") {
		t.Fatalf("header or overflow marker missing: %q", text)
	}
}

func TestTextRendererStartsContinuationAfterCompletion(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	for _, event := range []Event{
		{Sequence: 1, RequestID: "req_long", Tool: "command_execute", Type: TypeToolCompleted, Input: []byte(`{"command":"sleep 1"}`), Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"started"}]}}`)},
		{Sequence: 2, RequestID: "req_long", Tool: "command_execute", Type: TypeCommandOutput, Stream: "stdout", Output: []byte(`{"text":"late output"}`)},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "╭─") != 2 || !strings.Contains(text, "continued") {
		t.Fatalf("continuation block missing: %q", text)
	}
}

func TestTextRendererUsesIndependentFallbackKeysAndResetsAfterGap(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	for _, event := range []Event{
		{Sequence: 1, Type: TypeObserverNotice, Summary: "first"},
		{Sequence: 2, Type: TypeObserverNotice, Summary: "second"},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(output.String(), "╭─") != 2 {
		t.Fatalf("fallback events were merged: %q", output.String())
	}

	renderer.ResetAfterGap()
	if err := renderer.RenderEvent(&output, Event{Sequence: 3, RequestID: "req_reset", Tool: "file_read", Type: TypeToolCompleted, Output: []byte(`{"status":"ok"}`)}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "╭─") != 3 {
		t.Fatalf("renderer state survived gap: %q", output.String())
	}
}
