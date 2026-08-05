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
		{Sequence: 42, RemoteSessionID: "4f8c2e90-6b2a-4b20-9d8c-1a1f8a12e7c4", RequestID: "req_42", Tool: "command_execute", Type: TypeToolStarted, Input: []byte(`{"command":"go test ./internal/auth"}`)},
	}
	for sequence := int64(43); sequence <= 100; sequence++ {
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
	bodyLines := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "│ ") {
			bodyLines++
		}
	}
	if bodyLines > maxInteractionBodyLines {
		t.Fatalf("body lines=%d output=%q", bodyLines, text)
	}
	if strings.Count(text, "╭─") != 1 || strings.Count(text, "╰") != 1 {
		t.Fatalf("interaction was not grouped: %q", text)
	}
	if !strings.Contains(text, "#42 · 4f8c2e90-6b2a-4b20-9d8c-1a1f8a12e7c4 · command_execute") || !strings.Contains(text, "...") {
		t.Fatalf("header or overflow marker missing: %q", text)
	}
	if !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("footer is not followed by blank line: %q", text)
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

func TestTextRendererSuppressesConsecutiveDuplicateProgressReports(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	progress := func(sequence int64, requestID, summary string) Event {
		return Event{
			Sequence: sequence, RequestID: requestID, Workspace: "demo", Tool: "progress_report",
			Type:   TypeToolCompleted,
			Input:  []byte(fmt.Sprintf(`{"summary":%q,"result_summary":"已删除顶层目录","status":"in_progress","next_step":"重新枚举","related_tool":"context_query"}`, summary)),
			Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"progress"}]}}`),
		}
	}
	for _, event := range []Event{
		progress(1, "req_1", "已完成目录级清理"),
		progress(2, "req_2", "已完成目录级清理"),
		progress(3, "req_3", "已完成最终验证"),
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "已完成目录级清理") != 1 {
		t.Fatalf("duplicate progress report was rendered: %q", text)
	}
	if strings.Count(text, "已完成最终验证") != 1 {
		t.Fatalf("distinct progress report was suppressed: %q", text)
	}
}

func TestTextRendererMergesCommandOutputChunksAndStreams(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	events := []Event{
		{Sequence: 1, RequestID: "req_output", Tool: "command_execute", Type: TypeToolStarted},
		{Sequence: 2, RequestID: "req_output", Tool: "command_execute", Type: TypeCommandOutput, Stream: "stdout", Output: []byte(`{"text":"first\n"}`)},
		{Sequence: 3, RequestID: "req_output", Tool: "command_execute", Type: TypeCommandOutput, Stream: "stdout", Offset: 6, Output: []byte(`{"text":"second\n"}`)},
		{Sequence: 4, RequestID: "req_output", Tool: "command_execute", Type: TypeCommandOutput, Stream: "stderr", Output: []byte(`{"text":"warning\n"}`)},
		{Sequence: 5, RequestID: "req_output", Tool: "command_execute", Type: TypeToolCompleted, Output: []byte(`{"status":"succeeded","result":{"content":[{"type":"text","text":"done"}]}}`)},
	}
	for _, event := range events {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "Read stdout") != 1 || strings.Count(text, "Read stderr") != 1 {
		t.Fatalf("stream headers were not merged: %q", text)
	}
	for _, want := range []string{"1 | first", "2 | second", "1 | warning"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output line %q missing: %q", want, text)
		}
	}
}

func TestTextRendererFoldsRepeatedReadEvents(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	input := []byte(`{"path":"src/demo.go"}`)
	result := []byte(`{"status":"succeeded","result":{"content":[{"type":"text","text":"Read 1 source item(s)."}]}}`)
	events := []Event{
		{Sequence: 1, RequestID: "req_read_1", Tool: "change_read", Type: TypeToolStarted, Input: input},
		{Sequence: 2, RequestID: "req_read_1", Tool: "change_read", Type: TypeToolCompleted, Status: "succeeded", Input: input, Output: result},
		{Sequence: 3, RequestID: "req_read_2", Tool: "change_read", Type: TypeToolStarted, Input: input},
		{Sequence: 4, RequestID: "req_read_2", Tool: "change_read", Type: TypeToolCompleted, Status: "succeeded", Input: input, Output: result},
		{Sequence: 5, RequestID: "req_other", Tool: "file_read", Type: TypeToolCompleted, Input: []byte(`{"path":"src/other.go"}`), Output: []byte(`{"status":"succeeded"}`)},
	}
	for _, event := range events {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "Read src/demo.go") != 1 || !strings.Contains(text, "Repeated src/demo.go x2") {
		t.Fatalf("repeated read was not folded: %q", text)
	}
}

func TestTextRendererAppliesSemanticFilters(t *testing.T) {
	renderer := NewTextRenderer(false)
	renderer.SetFilter(EventFilter{Tool: "change_read", Path: "demo.go"})
	var output bytes.Buffer
	for _, event := range []Event{
		{Sequence: 1, Tool: "file_read", Type: TypeToolCompleted, Path: "src/demo.go", Output: []byte(`{"status":"succeeded"}`)},
		{Sequence: 2, Tool: "change_read", Type: TypeToolCompleted, Path: "src/other.go", Output: []byte(`{"status":"succeeded"}`)},
		{Sequence: 3, Tool: "change_read", Type: TypeToolCompleted, Path: "src/demo.go", Output: []byte(`{"status":"succeeded"}`)},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	if text := output.String(); !strings.Contains(text, "change_read") || strings.Contains(text, "file_read") || strings.Contains(text, "other.go") {
		t.Fatalf("semantic filter output=%q", text)
	}
}

func TestFormatRemoteSessionIDUsesCanonicalUUID(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "uuid", input: "4F8C2E90-6B2A-4B20-9D8C-1A1F8A12E7C4", want: "4f8c2e90-6b2a-4b20-9d8c-1a1f8a12e7c4"},
		{name: "legacy random id", input: "rs_AAECAwQFBgcICQoLDA0ODw", want: "00010203-0405-0607-0809-0a0b0c0d0e0f"},
		{name: "unknown id", input: "rs_observer", want: "rs_observer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := formatRemoteSessionID(test.input); got != test.want {
				t.Fatalf("formatted remote session id=%q, want %q", got, test.want)
			}
		})
	}
}

func TestTextRendererAllowsFiftyBodyLinesBeforeEllipsis(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	block := &interactionBlock{key: "test", sequence: 1, tool: "file_read"}
	renderer.blocks[block.key] = block
	if err := renderer.activate(&output, block); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxInteractionBodyLines; index++ {
		if err := renderer.writeBodyLine(&output, block, fmt.Sprintf("line-%02d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderer.close(&output, block); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "│ ...") {
		t.Fatalf("exactly twenty body lines should not truncate: %q", text)
	}
	if strings.Count(text, "│ ") != maxInteractionBodyLines || !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("body/footer budget output=%q", text)
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

func TestTextRendererClipsBodyAndFillsFooterToConfiguredWidth(t *testing.T) {
	const width = 32
	renderer := NewTextRendererWithWidth(false, width)
	var output bytes.Buffer
	if err := renderer.RenderEvent(&output, Event{
		Sequence:  8,
		RequestID: "req_width",
		Tool:      "context_query",
		Type:      TypeToolCompleted,
		Input:     []byte(`{"action":"search","query":"find a very long query"}`),
		Output:    []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"This is a deliberately long result line that must be clipped to the terminal width."}]}}`),
	}); err != nil {
		t.Fatal(err)
	}

	trimmed := strings.TrimSuffix(strings.TrimSuffix(output.String(), "\n"), "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 {
		t.Fatal("renderer returned no lines")
	}
	for _, line := range lines {
		if got := displayWidth(line); got > width {
			t.Fatalf("line width=%d exceeds %d: %q", got, width, line)
		}
	}
	footer := ""
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) != "" {
			footer = lines[index]
			break
		}
	}
	if !strings.HasPrefix(footer, "╰") || displayWidth(footer) != width {
		t.Fatalf("footer width=%d content=%q", displayWidth(footer), footer)
	}
	if !strings.Contains(output.String(), "...") {
		t.Fatalf("long body line was not truncated: %q", output.String())
	}
}

func TestTextRendererRefreshesWidthAfterTerminalResize(t *testing.T) {
	renderer := NewTextRendererWithWidth(false, 80)
	renderer.SetWidth(28)
	var output bytes.Buffer
	if err := renderer.RenderEvent(&output, Event{
		Sequence:  9,
		RequestID: "req_resize",
		Tool:      "progress_report",
		Type:      TypeToolCompleted,
		Input:     []byte(`{"summary":"报告进度"}`),
		Output:    []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"这是一段在终端缩窄后也不能从左侧溢出的长文本。"}]}}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if got := displayWidth(line); got > 28 {
			t.Fatalf("resized line width=%d, want <= 28: %q", got, line)
		}
	}
}

func TestTextRendererClipsPendingLineAfterTerminalResize(t *testing.T) {
	renderer := NewTextRendererWithWidth(false, 80)
	var output bytes.Buffer
	block := &interactionBlock{key: "pending_resize", sequence: 10, tool: "file_read"}
	renderer.blocks[block.key] = block
	if err := renderer.activate(&output, block); err != nil {
		t.Fatal(err)
	}
	if err := renderer.writeBodyLine(&output, block, "this pending line was collected before the resize"); err != nil {
		t.Fatal(err)
	}
	renderer.SetWidth(28)
	if err := renderer.close(&output, block); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if got := displayWidth(line); got > 28 {
			t.Fatalf("pending line width=%d, want <= 28: %q", got, line)
		}
	}
}
