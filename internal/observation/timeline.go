package observation

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

// TextRenderer groups related observation events into one bounded terminal
// block. It is intentionally stateful because a command's output can arrive
// between its tool.started and tool.completed events.
type TextRenderer struct {
	colorMode               ColorMode
	width                   int
	diffMode                DiffMode
	diffCache               *diffDocumentCache
	filter                  EventFilter
	blocks                  map[string]*interactionBlock
	activeKey               string
	fallbackSeq             uint64
	lastProgressFingerprint string
	lastSemanticFingerprint string
	duplicateCount          int
	detail                  bool
}

type interactionBlock struct {
	key              string
	sequence         int64
	remoteSessionID  string
	tool             string
	failed           bool
	continuation     bool
	opened           bool
	closed           bool
	bodyLines        int
	pendingLine      string
	ellipsis         bool
	commandOutput    bool
	status           string
	outputLines      map[string]int
	lastOutputStream string
}

// NewTextRenderer creates a text renderer for one observer stream.
func NewTextRenderer(color bool) *TextRenderer {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return NewTextRendererWithMode(mode, 0)
}

// NewTextRendererWithWidth creates a text renderer constrained to terminalWidth
// display cells. A non-positive width uses the renderer's safe fallback width.
func NewTextRendererWithWidth(color bool, terminalWidth int) *TextRenderer {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return NewTextRendererWithMode(mode, terminalWidth)
}

// NewTextRendererWithMode creates a renderer with explicit ANSI capabilities.
func NewTextRendererWithMode(mode ColorMode, terminalWidth int) *TextRenderer {
	if terminalWidth <= 0 {
		terminalWidth = defaultTerminalWidth
	}
	if terminalWidth < 4 {
		terminalWidth = 4
	}
	return &TextRenderer{
		colorMode: mode,
		width:     terminalWidth,
		diffMode:  DiffModePreview,
		diffCache: newDiffDocumentCache(),
		blocks:    make(map[string]*interactionBlock),
	}
}

// SetWidth refreshes the terminal width without discarding open interaction
// blocks. Callers that observe terminal resize events can update the renderer
// before writing the next frame; non-positive values leave the current width
// unchanged.
func (r *TextRenderer) SetWidth(terminalWidth int) {
	if r == nil || terminalWidth <= 0 {
		return
	}
	if terminalWidth < 4 {
		terminalWidth = 4
	}
	r.width = terminalWidth
}

// SetDetail enables explicit semantic-purpose and operation metadata in the
// terminal stream. It never exposes hidden model chain-of-thought.
func (r *TextRenderer) SetDetail(detail bool) {
	if r != nil {
		r.detail = detail
	}
}

// SetDiffMode changes the amount of file diff detail rendered for future
// events. Summary is useful for high-volume observation streams; full keeps
// every inline hunk.
func (r *TextRenderer) SetDiffMode(mode DiffMode) {
	if r != nil {
		r.diffMode = mode
	}
}

// SetFilter applies client-side semantic filters without changing the
// durable observer cursor or server-side event history.
func (r *TextRenderer) SetFilter(filter EventFilter) {
	if r == nil {
		return
	}
	r.filter = EventFilter{
		Tool:        strings.ToLower(strings.TrimSpace(filter.Tool)),
		Status:      strings.ToLower(strings.TrimSpace(filter.Status)),
		OperationID: strings.TrimSpace(filter.OperationID),
		Path:        strings.ToLower(strings.TrimSpace(filter.Path)),
	}
}

// RenderEvent writes one event, keeping the block open until the interaction
// completes or another interaction needs the active terminal span.
func (r *TextRenderer) RenderEvent(w io.Writer, event Event) error {
	if r == nil {
		return fmt.Errorf("text renderer is required")
	}
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	if r.blocks == nil {
		r.blocks = make(map[string]*interactionBlock)
	}
	if !eventMatchesFilter(event, r.filter) {
		return nil
	}
	if event.Tool == "progress_report" && event.Type == TypeToolCompleted {
		fingerprint := progressFingerprint(event)
		if fingerprint != "" && fingerprint == r.lastProgressFingerprint {
			return nil
		}
		r.lastProgressFingerprint = fingerprint
	} else if event.Tool != "progress_report" || event.Type != TypeToolStarted {
		r.lastProgressFingerprint = ""
	}
	if duplicate, count := r.duplicateEvent(event); duplicate {
		return r.renderDuplicateNotice(w, event, count)
	}
	key := r.eventKey(event)
	block := r.blocks[key]
	if block == nil || block.closed {
		block = &interactionBlock{
			key:             key,
			sequence:        event.Sequence,
			remoteSessionID: formatRemoteSessionID(event.RemoteSessionID),
			tool:            event.toolOrType(),
			failed:          eventFailed(event),
			status:          eventStatus(event),
			continuation:    block != nil && block.closed,
			outputLines:     make(map[string]int),
		}
		r.blocks[key] = block
	} else {
		if event.RemoteSessionID != "" {
			block.remoteSessionID = formatRemoteSessionID(event.RemoteSessionID)
		}
		if event.Tool != "" {
			block.tool = event.Tool
		}
		block.failed = block.failed || eventFailed(event)
		if status := eventStatus(event); status != "" {
			block.status = status
		}
	}

	if event.Type == TypeToolStarted {
		if event.Tool != "" {
			block.tool = event.Tool
		}
		return nil
	}
	wasCommandOutput := block.commandOutput
	if event.Type == TypeCommandOutput {
		block.commandOutput = true
	}

	var rendered bytes.Buffer
	stream := strings.TrimSpace(event.Stream)
	if stream == "" {
		stream = "output"
	}
	outputLineStart := 0
	suppressOutputAction := false
	if event.Type == TypeCommandOutput {
		outputLineStart = block.outputLines[stream] + 1
		suppressOutputAction = wasCommandOutput && block.lastOutputStream == stream
	}
	if err := renderTextWithOptions(&rendered, event, renderOptions{
		colorMode:            r.colorMode,
		terminalWidth:        r.width,
		detail:               r.detail,
		diffMode:             r.diffMode,
		diffCache:            r.diffCache,
		suppressOutputAction: suppressOutputAction,
		outputLineStart:      outputLineStart,
	}, block.commandOutput && event.Type == TypeToolCompleted); err != nil {
		return err
	}
	lines := splitRenderedLines(rendered.String())
	if len(lines) == 0 {
		return nil
	}
	if err := r.activate(w, block); err != nil {
		return err
	}
	for _, line := range lines {
		if err := r.writeBodyLine(w, block, line); err != nil {
			return err
		}
	}
	if event.Type == TypeCommandOutput {
		block.outputLines[stream] += commandOutputLineCount(event)
		block.lastOutputStream = stream
	}
	switch event.Type {
	case TypeCommandOutput, TypeFileChanged:
		return nil
	default:
		return r.close(w, block)
	}
}

// ResetAfterGap discards incomplete interaction state after the client has
// reconnected and replayed the durable sequence range.
func (r *TextRenderer) ResetAfterGap() {
	if r == nil {
		return
	}
	r.blocks = make(map[string]*interactionBlock)
	r.activeKey = ""
	r.fallbackSeq = 0
	r.lastProgressFingerprint = ""
	r.lastSemanticFingerprint = ""
	r.duplicateCount = 0
}

func commandOutputLineCount(event Event) int {
	var payload struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(event.Output, &payload) != nil || payload.Text == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(payload.Text, "\n"), "\n"))
}

func (r *TextRenderer) duplicateEvent(event Event) (bool, int) {
	fingerprint := semanticEventFingerprint(event)
	if fingerprint == "" {
		if event.Type == TypeToolStarted {
			return false, 0
		}
		r.lastSemanticFingerprint = ""
		r.duplicateCount = 0
		return false, 0
	}
	if fingerprint == r.lastSemanticFingerprint {
		r.duplicateCount++
		return true, r.duplicateCount
	}
	r.lastSemanticFingerprint = fingerprint
	r.duplicateCount = 1
	return false, 0
}

func semanticEventFingerprint(event Event) string {
	if event.Type != TypeToolCompleted || !deduplicableTool(event.Tool) {
		return ""
	}
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	result, _ := payload["result"].(map[string]any)
	failed, failure := toolFailure(payload)
	parts := []string{
		event.Tool,
		event.Status,
		string(event.Input),
		event.Path,
	}
	if failed {
		parts = append(parts, "failed", failure)
	} else if result != nil {
		parts = append(parts, humanToolOutput(event.Tool, result))
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))
}

func deduplicableTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "change_read", "change_prepare", "source_read", "file_read", "context_query", "workspace_state", "runtime_read":
		return true
	default:
		return false
	}
}

func (r *TextRenderer) renderDuplicateNotice(w io.Writer, event Event, count int) error {
	_, label := toolAction(event.Tool, event.Input)
	color := eventActionColor(event, actionColor(event.Tool, false))
	_, err := fmt.Fprintf(w, "  %s %s %s x%d\n", paint("↳", color, r.colorMode != ColorModeNone), paint("Repeated", color, r.colorMode != ColorModeNone), compactLine(label), count)
	return err
}

func progressFingerprint(event Event) string {
	var input struct {
		Summary       string `json:"summary"`
		ResultSummary string `json:"result_summary"`
		Status        string `json:"status"`
		NextStep      string `json:"next_step"`
		RelatedTool   string `json:"related_tool"`
	}
	if err := json.Unmarshal(event.Input, &input); err != nil {
		return ""
	}
	values := []string{
		event.Workspace, event.RemoteSessionID, input.Summary, input.ResultSummary,
		input.Status, input.NextStep, input.RelatedTool,
	}
	if strings.TrimSpace(input.Summary) == "" && strings.TrimSpace(event.ProgressSummary) == "" {
		return ""
	}
	return strings.Join(values, "\x00")
}

func (r *TextRenderer) eventKey(event Event) string {
	if event.RequestID != "" {
		return "request:" + event.RequestID
	}
	if event.OperationID != "" {
		return "operation:" + event.OperationID
	}
	if event.Sequence != 0 {
		return fmt.Sprintf("sequence:%d", event.Sequence)
	}
	r.fallbackSeq++
	return fmt.Sprintf("event:%d", r.fallbackSeq)
}

func eventMatchesFilter(event Event, filter EventFilter) bool {
	if filter.Tool != "" && strings.ToLower(strings.TrimSpace(event.Tool)) != filter.Tool {
		return false
	}
	if filter.OperationID != "" && event.OperationID != filter.OperationID {
		return false
	}
	if filter.Status != "" && eventStatus(event) != filter.Status {
		return false
	}
	if filter.Path != "" && !strings.Contains(strings.ToLower(eventPath(event)), filter.Path) {
		return false
	}
	return true
}

func eventPath(event Event) string {
	if strings.TrimSpace(event.Path) != "" {
		return event.Path
	}
	input := inputMap(event.Input)
	if path, ok := input["path"].(string); ok && strings.TrimSpace(path) != "" {
		return path
	}
	paths := make([]string, 0, 4)
	for _, key := range []string{"items", "operations"} {
		values, _ := input[key].([]any)
		for _, raw := range values {
			item, _ := raw.(map[string]any)
			for _, pathKey := range []string{"path", "new_path"} {
				if path, ok := item[pathKey].(string); ok && strings.TrimSpace(path) != "" {
					paths = append(paths, path)
				}
			}
		}
	}
	if len(paths) > 0 {
		return strings.Join(paths, " ")
	}
	if values, ok := input["paths"].([]any); ok {
		for _, raw := range values {
			if path, ok := raw.(string); ok && strings.TrimSpace(path) != "" {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) > 0 {
		return strings.Join(paths, " ")
	}
	if event.Type != TypeFileChanged {
		return ""
	}
	var payload struct {
		Files []struct {
			Path    string `json:"path"`
			NewPath string `json:"new_path"`
		} `json:"files"`
	}
	if json.Unmarshal(event.Output, &payload) != nil {
		return ""
	}
	paths = make([]string, 0, len(payload.Files)*2)
	for _, file := range payload.Files {
		paths = append(paths, file.Path, file.NewPath)
	}
	return strings.Join(paths, " ")
}

func (r *TextRenderer) activate(w io.Writer, block *interactionBlock) error {
	if r.activeKey != "" && r.activeKey != block.key {
		if active := r.blocks[r.activeKey]; active != nil && !active.closed {
			if err := r.close(w, active); err != nil {
				return err
			}
		}
	}
	if block.opened {
		r.activeKey = block.key
		return nil
	}
	header := "╭─"
	if block.sequence == 0 {
		if block.remoteSessionID == "" {
			header += " · "
		}
	} else {
		header += fmt.Sprintf(" #%d", block.sequence)
	}
	for _, part := range []string{block.remoteSessionID, block.tool} {
		if part != "" {
			header += " · " + part
		}
	}
	if block.continuation {
		header += " · continued"
	}
	header = truncateRenderedLine(header, r.width)
	if _, err := fmt.Fprintln(w, paint(header, blockColor(block), r.colorMode != ColorModeNone)); err != nil {
		return err
	}
	block.opened = true
	r.activeKey = block.key
	return nil
}

// formatRemoteSessionID keeps old persisted rs_ identifiers readable while
// rendering newly-created UUID session identifiers in canonical form.
func formatRemoteSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := uuid.Parse(value); err == nil {
		return parsed.String()
	}
	if strings.HasPrefix(value, "rs_") {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "rs_"))
		if err == nil && len(raw) == 16 {
			var parsed uuid.UUID
			copy(parsed[:], raw)
			return parsed.String()
		}
	}
	return value
}

func (r *TextRenderer) writeBodyLine(w io.Writer, block *interactionBlock, line string) error {
	if strings.TrimSpace(stripANSI(line)) == "" {
		return nil
	}
	bodyWidth := r.width - 2
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	line = truncateRenderedLine(line, bodyWidth)
	if block.ellipsis {
		return nil
	}
	if block.pendingLine == "" {
		block.pendingLine = line
		return nil
	}
	if block.bodyLines >= maxInteractionBodyLines-1 {
		block.pendingLine = ""
		marker := truncateRenderedLine("│ ...", r.width)
		if _, err := fmt.Fprintln(w, paint(marker, ansiYellow, r.colorMode != ColorModeNone)); err != nil {
			return err
		}
		block.bodyLines++
		block.ellipsis = true
		return nil
	}
	if err := r.flushBodyLine(w, block, block.pendingLine); err != nil {
		return err
	}
	block.pendingLine = line
	return nil
}

func (r *TextRenderer) flushBodyLine(w io.Writer, block *interactionBlock, line string) error {
	bodyWidth := r.width - 2
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	line = truncateRenderedLine(line, bodyWidth)
	if _, err := fmt.Fprintln(w, "│ "+line); err != nil {
		return err
	}
	block.bodyLines++
	return nil
}

func (r *TextRenderer) close(w io.Writer, block *interactionBlock) error {
	if block == nil || block.closed {
		return nil
	}
	if block.pendingLine != "" && !block.ellipsis {
		if err := r.flushBodyLine(w, block, block.pendingLine); err != nil {
			return err
		}
		block.pendingLine = ""
	}
	footer := "╰" + strings.Repeat("─", r.width-1)
	if _, err := fmt.Fprintln(w, paint(footer, blockColor(block), r.colorMode != ColorModeNone)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	block.closed = true
	if r.activeKey == block.key {
		r.activeKey = ""
	}
	return nil
}

func blockColor(block *interactionBlock) string {
	if block == nil {
		return ansiGray
	}
	return eventActionColor(Event{Tool: block.tool, Status: block.status}, actionColor(block.tool, block.failed))
}

func splitRenderedLines(value string) []string {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func eventFailed(event Event) bool {
	if event.Type != TypeToolCompleted {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(event.Output, &payload) != nil {
		return false
	}
	failed, _ := toolFailure(payload)
	return failed
}

func stripANSI(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\033' {
			builder.WriteByte(value[index])
			continue
		}
		for index+1 < len(value) {
			index++
			if value[index] == 'm' {
				break
			}
		}
	}
	return builder.String()
}
