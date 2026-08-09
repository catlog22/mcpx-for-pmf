package observation

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func joinWrappedObservationLines(text string) string {
	var builder strings.Builder
	for _, line := range strings.Split(text, "\n") {
		builder.WriteString(strings.TrimLeft(line, " "))
	}
	return builder.String()
}

func TestTextRendererGroupsInteractionIntoBoundedBlock(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	events := []Event{
		{Sequence: 42, RemoteSessionID: "4f8c2e90-6b2a-4b20-9d8c-1a1f8a12e7c4", RequestID: "req_42", Tool: "command_execute", Type: TypeToolStarted, Input: []byte(`{"command":"go test ./internal/auth"}`)},
	}
	for sequence := int64(43); sequence <= 100; sequence++ {
		events = append(events, Event{
			Sequence: sequence, RequestID: "req_42", Tool: "command_execute", Type: TypeCommandOutput,
			Command: "go test ./internal/auth", Stream: "stdout", Output: []byte(fmt.Sprintf(`{"text":"line-%d-a\nline-%d-b","bytes":17}`, sequence, sequence)),
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
	if strings.Contains(text, "╭─") || strings.Contains(text, "╰") || strings.Contains(text, "│ ") {
		t.Fatalf("compact renderer leaked framed timeline: %q", text)
	}
	if strings.Count(text, "Ran go test ./internal/auth") != 1 || !strings.Contains(text, "↳ stdout:") || !strings.Contains(text, "output truncated") {
		t.Fatalf("command-centric compact output or overflow marker missing: %q", text)
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
	if strings.Count(text, "Read stdout") != 1 || strings.Contains(text, "continued") {
		t.Fatalf("continuation was not rendered as a compact stream: %q", text)
	}
}

func TestTextRendererSeparatesAdjacentOperationsWithToolAndStatus(t *testing.T) {
	renderer := NewTextRendererWithMode(ColorModeANSI16, 80)
	renderer.SetDetail(true)
	var output bytes.Buffer
	for _, event := range []Event{
		{Sequence: 1, RequestID: "req_read", Tool: "read", Type: TypeToolCompleted, Status: "succeeded", Input: []byte(`{"view":"file","path":"a.go"}`), Output: []byte(`{"status":"succeeded","result":{"content":[{"type":"text","text":"Read a.go."}]}}`)},
		{Sequence: 2, RequestID: "req_edit", Tool: "edit", Type: TypeToolCompleted, Status: "failed", Input: []byte(`{"path":"a.go"}`), Output: []byte(`{"status":"failed","result":{"content":[{"type":"text","text":"edit failed"}]}}`)},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if !strings.Contains(text, "── edit · failed") {
		t.Fatalf("operation separator missing tool/status: %q", text)
	}
	if !strings.Contains(text, ansiRed) {
		t.Fatalf("failed operation did not use error color: %q", text)
	}
	if strings.Count(text, ansiReset) < 4 {
		t.Fatalf("colored adjacent operations were not independently reset: %q", text)
	}
}

func TestTextRendererDefaultHidesOperationAndDurationTelemetry(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	for _, event := range []Event{
		{Sequence: 1, RequestID: "req_1", Tool: "read", Type: TypeToolCompleted, Status: "succeeded", Input: []byte(`{"view":"file","path":"a.go"}`), Output: []byte(`{"status":"succeeded"}`)},
		{Sequence: 2, RequestID: "req_2", OperationID: "op_secret", Tool: "execute", Type: TypeToolCompleted, Status: "succeeded", DurationMs: 27, Command: "go test ./...", Input: []byte(`{"command":"go test ./..."}`), Output: []byte(`{"status":"succeeded"}`)},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if !strings.Contains(text, "• Ran go test ./...") {
		t.Fatalf("real command missing: %q", text)
	}
	for _, forbidden := range []string{"operation=op_secret", "duration=27ms", "── execute"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("default telemetry leaked %q: %q", forbidden, text)
		}
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

func TestTextRendererSuppressesProtocolNoiseButKeepsFailedOperation(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	events := []Event{
		{Sequence: 1, OperationID: "op_1", Type: TypeOperationStarted, Status: "running", Summary: "operation queued"},
		{Sequence: 2, OperationID: "op_1", StepID: "step_1", Type: TypeOperationStepStarted, Status: "running", Summary: "operation step started"},
		{Sequence: 3, OperationID: "op_1", StepID: "step_1", Type: TypeOperationStepCompleted, Status: "succeeded", Summary: "operation step succeeded"},
		{Sequence: 4, OperationID: "op_1", Type: TypeOperationCompleted, Status: "succeeded", Summary: "operation succeeded"},
		{Sequence: 5, Type: TypeObserverNotice, Summary: "command.started: go test ./...", Output: []byte(`{"source_type":"command.started"}`)},
		{Sequence: 6, OperationID: "op_2", Type: TypeOperationCompleted, Status: "failed", Summary: "operation failed"},
	}
	for _, event := range events {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Contains(text, "operation queued") || strings.Contains(text, "step started") || strings.Contains(text, "command.started") {
		t.Fatalf("protocol noise leaked into compact output: %q", text)
	}
	if !strings.Contains(text, "Observed operation failed") {
		t.Fatalf("failed operation result was hidden: %q", text)
	}
}

func TestHumanTextStripsTerminalControlsWithoutChangingJSON(t *testing.T) {
	event := Event{
		Tool:   "command_execute",
		Type:   TypeToolCompleted,
		Output: []byte("{\"status\":\"ok\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\\u001b[Z^[[?25lworld\\r\"}]}}"),
	}
	var textOutput bytes.Buffer
	if err := RenderText(&textOutput, event, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(textOutput.String(), "\033") || strings.Contains(textOutput.String(), "^[[") || strings.Contains(textOutput.String(), "\\r") {
		t.Fatalf("terminal controls leaked: %q", textOutput.String())
	}
	var jsonOutput bytes.Buffer
	if err := RenderJSON(&jsonOutput, event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOutput.String(), `\u001b`) {
		t.Fatalf("JSON event was unexpectedly sanitized: %q", jsonOutput.String())
	}
}

func TestTextRendererMergesCommandOutputChunksAndStreams(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	events := []Event{
		{Sequence: 1, RequestID: "req_output", Tool: "command_execute", Type: TypeToolStarted, Input: []byte(`{"command":"go test ./..."}`)},
		{Sequence: 2, RequestID: "req_output", Tool: "command_execute", Type: TypeCommandOutput, Command: "go test ./...", Stream: "stdout", Output: []byte(`{"text":"first\n"}`)},
		{Sequence: 3, RequestID: "req_output", Tool: "command_execute", Type: TypeCommandOutput, Command: "go test ./...", Stream: "stdout", Offset: 6, Output: []byte(`{"text":"second\n"}`)},
		{Sequence: 4, RequestID: "req_output", Tool: "command_execute", Type: TypeCommandOutput, Command: "go test ./...", Stream: "stderr", Output: []byte(`{"text":"warning\n"}`)},
		{Sequence: 5, RequestID: "req_output", Tool: "command_execute", Type: TypeToolCompleted, Command: "go test ./...", Output: []byte(`{"status":"succeeded","result":{"content":[{"type":"text","text":"done"}]}}`)},
	}
	for _, event := range events {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "Ran go test ./...") != 1 || strings.Count(text, "↳ stdout:") != 1 || strings.Count(text, "↳ stderr:") != 1 {
		t.Fatalf("command/stream headers were not merged into one block: %q", text)
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
		{Sequence: 1, RequestID: "req_read_1", Tool: "read", Type: TypeToolStarted, Input: input},
		{Sequence: 2, RequestID: "req_read_1", Tool: "read", Type: TypeToolCompleted, Status: "succeeded", Input: input, Output: result},
		{Sequence: 3, RequestID: "req_read_2", Tool: "read", Type: TypeToolStarted, Input: input},
		{Sequence: 4, RequestID: "req_read_2", Tool: "read", Type: TypeToolCompleted, Status: "succeeded", Input: input, Output: result},
		{Sequence: 5, RequestID: "req_other", Tool: "file_read", Type: TypeToolCompleted, Input: []byte(`{"path":"src/other.go"}`), Output: []byte(`{"status":"succeeded"}`)},
	}
	for _, event := range events {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Count(text, "Read src/demo.go (full)") != 1 || !strings.Contains(text, "Repeated src/demo.go (full) x2") {
		t.Fatalf("repeated read was not folded: %q", text)
	}
}

func TestTextRendererShowsProgressBeforeCompletionOnce(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	progress := "已完成定位，下一步运行测试"
	for _, event := range []Event{
		{Sequence: 0, RequestID: "req_previous", Tool: "read", Type: TypeToolCompleted, Status: "succeeded", Output: []byte(`{"status":"succeeded"}`)},
		{Sequence: 1, RequestID: "req_progress", Tool: "execute", Type: TypeToolStarted, Goal: "验证变更", Purpose: "运行测试", ReasoningSummary: "先验证最小闭环", ProgressSummary: progress, NextStep: "检查失败日志", PlanID: "pl_progress", PlanTaskID: "pt_progress", ExecutionTaskID: "task_progress", OperationID: "op_progress"},
		{Sequence: 2, RequestID: "req_progress", Tool: "execute", Type: TypeToolCompleted, Status: "succeeded", DurationMs: 27, Goal: "验证变更", Purpose: "运行测试", ReasoningSummary: "先验证最小闭环", ProgressSummary: progress, NextStep: "检查失败日志", PlanID: "pl_progress", PlanTaskID: "pt_progress", ExecutionTaskID: "task_progress", OperationID: "op_progress", Command: "go test ./...", Input: []byte(`{"command":"go test ./..."}`), Output: []byte(`{"status":"succeeded"}`)},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	text := output.String()
	if strings.Contains(text, "Progress model summary") || strings.Count(text, "已完成定位，下一步运行测试") != 1 {
		t.Fatalf("progress rendering=%q", text)
	}
	for _, want := range []string{"goal: 验证变更", "purpose: 运行测试", "reasoning: 先验证最小闭环", "next: 检查失败日志"} {
		if !strings.Contains(text, want) {
			t.Fatalf("semantic context missing %q: %q", want, text)
		}
	}
	if !strings.Contains(text, "goal: 验证变更 · purpose: 运行测试") || !strings.Contains(text, "plan: pl_progress · plan task: pt_progress · execution task: task_progress") || !strings.Contains(text, "• Ran go test ./...") {
		t.Fatalf("semantic context or command was not grouped: %q", text)
	}
	for _, forbidden := range []string{"operation=op_progress", "duration=27ms", "↳ operation: op_progress"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("default telemetry leaked %q: %q", forbidden, text)
		}
	}
}

func TestTextRendererAppliesSemanticFilters(t *testing.T) {
	renderer := NewTextRenderer(false)
	renderer.SetFilter(EventFilter{Tool: "read", Path: "demo.go"})
	var output bytes.Buffer
	for _, event := range []Event{
		{Sequence: 1, Tool: "file_read", Type: TypeToolCompleted, Path: "src/demo.go", Output: []byte(`{"status":"succeeded"}`)},
		{Sequence: 2, Tool: "read", Type: TypeToolCompleted, Path: "src/other.go", Output: []byte(`{"status":"succeeded"}`)},
		{Sequence: 3, Tool: "read", Type: TypeToolCompleted, Path: "src/demo.go", Output: []byte(`{"status":"succeeded"}`)},
	} {
		if err := renderer.RenderEvent(&output, event); err != nil {
			t.Fatal(err)
		}
	}
	if text := output.String(); !strings.Contains(strings.ToLower(text), "read") || strings.Contains(text, "file_read") || strings.Contains(text, "other.go") {
		t.Fatalf("semantic filter output=%q", text)
	}
}

func TestTextRendererAllowsFiftyBodyLinesBeforeEllipsis(t *testing.T) {
	renderer := NewTextRenderer(false)
	var output bytes.Buffer
	block := &interactionBlock{key: "test"}
	renderer.blocks[block.key] = block
	if err := renderer.activate(&output, block, Event{Tool: "command_execute", Status: "running"}); err != nil {
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
	if strings.Contains(text, "output truncated") {
		t.Fatalf("exactly fifty body lines should not truncate: %q", text)
	}
	if strings.Count(text, "line-") != maxInteractionBodyLines || !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("body/footer budget output=%q", text)
	}
}

func TestTextRendererCapsWrappedBodyLines(t *testing.T) {
	renderer := NewTextRendererWithWidth(false, 10)
	var output bytes.Buffer
	block := &interactionBlock{key: "wrapped"}
	if err := renderer.activate(&output, block, Event{Tool: "command_execute", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.writeBodyLine(&output, block, strings.Repeat("x", 500)); err != nil {
		t.Fatal(err)
	}
	if err := renderer.close(&output, block); err != nil {
		t.Fatal(err)
	}
	if block.bodyLines != maxInteractionBodyLines || !block.ellipsis {
		t.Fatalf("wrapped body budget=%d ellipsis=%v, want %d and true", block.bodyLines, block.ellipsis, maxInteractionBodyLines)
	}
	if got := strings.Count(output.String(), "xxxxxxxx"); got == 0 || !strings.Contains(output.String(), "… out") {
		t.Fatalf("wrapped output did not retain content and truncation marker: %q", output.String())
	}
}

func TestWrapRenderedLinePreservesANSIStyles(t *testing.T) {
	value := ansiRed + "abcdefgh" + ansiReset
	segments := wrapRenderedLine(value, 4)
	if len(segments) != 2 {
		t.Fatalf("segments=%d, want 2: %#v", len(segments), segments)
	}
	var plain strings.Builder
	for index, segment := range segments {
		if got := displayWidth(segment); got > 4 {
			t.Fatalf("segment %d width=%d, want <= 4: %q", index, got, segment)
		}
		if !strings.HasSuffix(segment, ansiReset) {
			t.Fatalf("segment %d does not reset ANSI style: %q", index, segment)
		}
		if index > 0 && !strings.HasPrefix(segment, ansiRed) {
			t.Fatalf("continuation %d did not reopen ANSI style: %q", index, segment)
		}
		plain.WriteString(stripANSI(segment))
	}
	if got := plain.String(); got != "abcdefgh" {
		t.Fatalf("wrapped text=%q, want %q", got, "abcdefgh")
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
	if strings.Count(output.String(), "Observed ") != 2 {
		t.Fatalf("fallback events were merged: %q", output.String())
	}

	renderer.ResetAfterGap()
	if err := renderer.RenderEvent(&output, Event{Sequence: 3, RequestID: "req_reset", Tool: "file_read", Type: TypeToolCompleted, Output: []byte(`{"status":"ok"}`)}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "╭─") || !strings.Contains(output.String(), "Read files") {
		t.Fatalf("renderer state survived gap: %q", output.String())
	}
}

func TestTextRendererWrapsBodyToConfiguredWidth(t *testing.T) {
	const width = 32
	renderer := NewTextRendererWithWidth(false, width)
	var output bytes.Buffer
	const longSummary = "This is a deliberately long result line that must remain complete in the observation."
	if err := renderer.RenderEvent(&output, Event{
		Sequence:  8,
		RequestID: "req_width",
		Tool:      "context_query",
		Type:      TypeToolCompleted,
		Input:     []byte(`{"action":"search","query":"find a very long query"}`),
		Output:    []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"This is a deliberately long result line that must remain complete in the observation."}]}}`),
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
	if !strings.Contains(joinWrappedObservationLines(output.String()), longSummary) {
		t.Fatalf("long body line was truncated: %q", output.String())
	}
}

func TestTextRendererKeepsSourceReadPathAndProgressSummaryComplete(t *testing.T) {
	const width = 40
	path := "fanyi-cloud/fanyi-module-erp/fanyi-module-erp-api/src/main/java/com/fanyi/cloud/module/erp/api/dto/StoreInfoApi.java"
	progress := "StoreInfoApi 没有按 merchantId 的现成查询；继续核对 StoreInfoRespDTO 是否包含 merchantId，并查商城实现。"
	renderer := NewTextRendererWithWidth(false, width)
	var output bytes.Buffer
	if err := renderer.RenderEvent(&output, Event{
		Sequence:        16707,
		RequestID:       "req_source_read",
		Tool:            "source_read",
		Type:            TypeToolCompleted,
		ProgressSummary: progress,
		Input:           []byte(`{"view":"file","path":"fanyi-cloud/fanyi-module-erp/fanyi-module-erp-api/src/main/java/com/fanyi/cloud/module/erp/api/dto/StoreInfoApi.java"}`),
		Output:          []byte(`{"status":"succeeded","result":{"content":[{"type":"text","text":"Read 1 source item(s); 42 bytes returned."}]}}`),
	}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	joined := joinWrappedObservationLines(text)
	if !strings.Contains(joined, path) {
		t.Fatalf("source path was truncated: %q", text)
	}
	if !strings.Contains(joined, progress) {
		t.Fatalf("progress summary was truncated: %q", text)
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if got := displayWidth(line); got > width {
			t.Fatalf("wrapped line width=%d, want <= %d: %q", got, width, line)
		}
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

func TestTextRendererUsesWidthWhenWritingAfterTerminalResize(t *testing.T) {
	renderer := NewTextRendererWithWidth(false, 80)
	var output bytes.Buffer
	block := &interactionBlock{key: "pending_resize"}
	renderer.blocks[block.key] = block
	if err := renderer.activate(&output, block, Event{Tool: "file_read", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	renderer.SetWidth(28)
	if err := renderer.writeBodyLine(&output, block, "this line is written after the resize"); err != nil {
		t.Fatal(err)
	}
	if err := renderer.close(&output, block); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if got := displayWidth(line); got > 28 {
			t.Fatalf("pending line width=%d, want <= 28: %q", got, line)
		}
	}
}
