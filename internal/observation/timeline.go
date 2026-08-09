package observation

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// TextRenderer renders a compact, line-oriented terminal stream. It remains
// stateful only for streamed command output, duplicate read folding, filters,
// and terminal-width bookkeeping; durable event grouping is left to JSON.
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
	lastClosedKey           string
	duplicateCount          int
	detail                  bool
	lastWorkspace           string
	lastRemoteSessionID     string
	lastGoal                string
}

type interactionBlock struct {
	key              string
	opened           bool
	closed           bool
	pendingEvent     Event
	pendingLines     []string
	pendingStarted   bool
	bodyLines        int
	ellipsis         bool
	commandOutput    bool
	fileChanged      bool
	outputLines      map[string]int
	lastOutputStream string
	contextShown     bool
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
		diffMode:  DiffModeFull,
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
	if !r.detail && isCompactObservationNoise(event) {
		return nil
	}
	goal := compactLine(event.Goal)
	suppressGoal := goal != "" && goal == r.lastGoal
	if goal != "" && !suppressGoal {
		r.lastGoal = goal
	}
	if err := r.writeTranscriptContext(w, event); err != nil {
		return err
	}
	if isProgressTool(event.Tool) && event.Type == TypeToolCompleted {
		fingerprint := progressFingerprint(event)
		if fingerprint != "" && fingerprint == r.lastProgressFingerprint {
			return nil
		}
		r.lastProgressFingerprint = fingerprint
	} else if !isProgressTool(event.Tool) || event.Type != TypeToolStarted {
		r.lastProgressFingerprint = ""
	}
	if duplicate, count := r.duplicateEvent(event); duplicate {
		return r.renderDuplicateNotice(w, event, count)
	}
	key := r.eventKey(event)
	block := r.blocks[key]
	if block == nil || block.closed {
		block = &interactionBlock{
			key:         key,
			outputLines: make(map[string]int),
		}
		r.blocks[key] = block
	}

	if event.Type == TypeToolStarted {
		if !hasSemanticContext(event) {
			return nil
		}
	}
	wasCommandOutput := block.commandOutput
	if event.Type == TypeCommandOutput {
		block.commandOutput = true
	}
	if event.Type == TypeFileChanged {
		block.fileChanged = true
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
		suppressAction:       event.Type == TypeToolCompleted && (block.commandOutput || block.fileChanged),
		suppressOutputAction: suppressOutputAction,
		commandOutputStarted: wasCommandOutput,
		suppressContext:      block.contextShown,
		suppressGoal:         suppressGoal,
		suppressDuration:     block.pendingStarted && event.Type == TypeToolCompleted,
		outputLineStart:      outputLineStart,
	}, block.commandOutput && event.Type == TypeToolCompleted); err != nil {
		return err
	}
	lines := splitRenderedLines(rendered.String())
	if len(lines) == 0 {
		return nil
	}
	if event.Type == TypeToolStarted {
		// Delay the operation header until the first output/completion event so
		// the header can carry the actual duration when a short call finishes.
		block.pendingEvent = event
		block.pendingLines = append([]string(nil), lines...)
		block.pendingStarted = true
		block.contextShown = true
		return nil
	}
	if block.pendingStarted {
		header := block.pendingEvent
		if event.DurationMs > 0 {
			header.DurationMs = event.DurationMs
		}
		if err := r.activate(w, block, header); err != nil {
			return err
		}
		for _, line := range block.pendingLines {
			if err := r.writeBodyLine(w, block, line); err != nil {
				return err
			}
		}
		block.pendingEvent = Event{}
		block.pendingLines = nil
		block.pendingStarted = false
	}
	if err := r.activate(w, block, event); err != nil {
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
	case TypeCommandOutput, TypeFileChanged, TypeToolStarted:
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
	r.lastClosedKey = ""
	r.duplicateCount = 0
	r.lastGoal = ""
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
	case "read", "source_read", "file_read", "context_query", "workspace_state", "runtime_read":
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
	view := progressEventView(event)
	if strings.TrimSpace(view.Current) == "" {
		return ""
	}
	values := []string{
		event.Workspace, event.RemoteSessionID, view.Current, view.Result,
		view.Status, view.Next, view.Phase, view.RelatedTool,
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
	for _, key := range []string{"items", "edits"} {
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
		Results []struct {
			Path    string `json:"path"`
			NewPath string `json:"new_path"`
		} `json:"results"`
	}
	if json.Unmarshal(event.Output, &payload) != nil {
		return ""
	}
	paths = make([]string, 0, len(payload.Results)*2)
	for _, file := range payload.Results {
		paths = append(paths, file.Path, file.NewPath)
	}
	return strings.Join(paths, " ")
}

func (r *TextRenderer) activate(w io.Writer, block *interactionBlock, event Event) error {
	if r.activeKey != "" && r.activeKey != block.key {
		if active := r.blocks[r.activeKey]; active != nil && !active.closed {
			if err := r.close(w, active); err != nil {
				return err
			}
		}
	}
	if !block.opened && r.lastClosedKey != "" && r.lastClosedKey != block.key {
		if r.detail {
			if err := r.writeOperationSeparator(w, event); err != nil {
				return err
			}
		} else if err := r.writeActionSeparator(w); err != nil {
			return err
		}
	}
	if block.opened {
		r.activeKey = block.key
		return nil
	}
	block.opened = true
	r.activeKey = block.key
	return nil
}

func (r *TextRenderer) writeBodyLine(w io.Writer, block *interactionBlock, line string) error {
	if strings.TrimSpace(stripANSI(line)) == "" {
		return nil
	}
	if block.ellipsis {
		return nil
	}
	if block.bodyLines >= maxInteractionBodyLines {
		return r.writeBodyEllipsis(w, block)
	}
	return r.flushBodyLine(w, block, line)
}

func (r *TextRenderer) flushBodyLine(w io.Writer, block *interactionBlock, line string) error {
	indentWidth := leadingSpaceWidth(line)
	continuationIndent := strings.Repeat(" ", indentWidth+2)
	bodyWidth := r.width - displayWidth(continuationIndent)
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	segments := wrapRenderedLine(line, bodyWidth)
	for index, segment := range segments {
		if block.bodyLines >= maxInteractionBodyLines {
			return r.writeBodyEllipsis(w, block)
		}
		if block.bodyLines == maxInteractionBodyLines-1 && index < len(segments)-1 {
			return r.writeBodyEllipsis(w, block)
		}
		if index > 0 {
			segment = continuationIndent + segment
		}
		if _, err := fmt.Fprintln(w, segment); err != nil {
			return err
		}
		block.bodyLines++
	}
	return nil
}

func leadingSpaceWidth(value string) int {
	width := 0
	for _, current := range value {
		if current != ' ' && current != '\t' {
			break
		}
		if current == '\t' {
			width += 4
		} else {
			width++
		}
	}
	return width
}

func (r *TextRenderer) writeBodyEllipsis(w io.Writer, block *interactionBlock) error {
	if block.ellipsis {
		return nil
	}
	marker := truncateRenderedLine("  … output truncated; use -format json for the complete event", r.width)
	if _, err := fmt.Fprintln(w, paint(marker, ansiYellow, r.colorMode != ColorModeNone)); err != nil {
		return err
	}
	if block.bodyLines < maxInteractionBodyLines {
		block.bodyLines++
	}
	block.ellipsis = true
	return nil
}

func (r *TextRenderer) close(w io.Writer, block *interactionBlock) error {
	if block == nil || block.closed {
		return nil
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	block.closed = true
	r.lastClosedKey = block.key
	if r.activeKey == block.key {
		r.activeKey = ""
	}
	return nil
}

func (r *TextRenderer) writeTranscriptContext(w io.Writer, event Event) error {
	workspace := strings.TrimSpace(event.Workspace)
	sessionID := strings.TrimSpace(event.RemoteSessionID)
	parts := make([]string, 0, 2)
	if workspace != "" && workspace != r.lastWorkspace {
		parts = append(parts, "Workspace "+workspace)
		r.lastWorkspace = workspace
	}
	if sessionID != "" && sessionID != r.lastRemoteSessionID {
		parts = append(parts, "Session "+sessionID)
		r.lastRemoteSessionID = sessionID
	}
	if len(parts) == 0 {
		return nil
	}
	line := "  " + strings.Join(parts, " · ")
	return writeSeparatorLine(w, line, ansiGray, r.colorMode)
}

func (r *TextRenderer) writeActionSeparator(w io.Writer) error {
	width := 24
	if r.width > 2 && r.width-2 < width {
		width = r.width - 2
	}
	if width < 2 {
		width = 2
	}
	return writeSeparatorLine(w, "  "+strings.Repeat("─", width), ansiGray, r.colorMode)
}

func (r *TextRenderer) writeOperationSeparator(w io.Writer, event Event) error {
	line := "  ── " + operationSeparatorLabel(event) + " " + strings.Repeat("─", 12)
	color := eventActionColor(event, actionColor(event.toolOrType(), false))
	return writeSeparatorLine(w, line, color, r.colorMode)
}

func writeSeparatorLine(w io.Writer, line, color string, mode ColorMode) error {
	if mode != ColorModeNone {
		line = paint(line, color, true)
	}
	_, err := fmt.Fprintln(w, line)
	return err
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
	return sanitizeTerminalText(value)
}

// sanitizeTerminalText removes terminal control sequences from text that is
// about to be shown to a human. Durable JSON events are never passed through
// this function, so scripts still receive the exact structured payload.
func sanitizeTerminalText(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\033' {
			index = skipTerminalSequence(value, index)
			continue
		}
		// Some log collectors render ESC as the printable two-byte marker "^[".
		// Treat the following CSI sequence the same way as a real ESC sequence.
		if value[index] == '^' && index+1 < len(value) && value[index+1] == '[' {
			index = skipTerminalSequence(value, index+1)
			continue
		}
		current := value[index]
		if current == '\n' {
			builder.WriteByte(current)
			index++
			continue
		}
		if current == '\t' {
			builder.WriteString("    ")
			index++
			continue
		}
		if current < 0x20 || current == 0x7f {
			index++
			continue
		}
		builder.WriteByte(current)
		index++
	}
	return builder.String()
}

func skipTerminalSequence(value string, start int) int {
	if start >= len(value) {
		return len(value)
	}
	index := start + 1
	if index < len(value) && value[index] == '[' {
		index++
		for index < len(value) {
			current := value[index]
			index++
			if current >= '@' && current <= '~' {
				return index
			}
		}
		return len(value)
	}
	if index < len(value) && value[index] == ']' {
		index++
		for index < len(value) {
			if value[index] == '\a' {
				return index + 1
			}
			if value[index] == '\033' && index+1 < len(value) && value[index+1] == '\\' {
				return index + 2
			}
			index++
		}
		return len(value)
	}
	if index < len(value) {
		return index + 1
	}
	return index
}

func isCompactObservationNoise(event Event) bool {
	switch event.Type {
	case TypeOperationStarted, TypeOperationStepStarted, TypeOperationStepCompleted:
		return true
	case TypeOperationCompleted:
		return !isErrorStatus(event.Status)
	case TypeObserverNotice:
		return compactNoticeNoise(event)
	default:
		return false
	}
}

func compactNoticeNoise(event Event) bool {
	sourceType := ""
	var payload struct {
		SourceType string `json:"source_type"`
	}
	if json.Unmarshal(event.Output, &payload) == nil {
		sourceType = strings.ToLower(strings.TrimSpace(payload.SourceType))
	}
	if sourceType == "" {
		sourceType = strings.ToLower(strings.TrimSpace(strings.SplitN(event.Summary, ":", 2)[0]))
	}
	return strings.HasSuffix(sourceType, ".started") ||
		strings.HasSuffix(sourceType, ".completed") ||
		strings.Contains(sourceType, ".step.")
}
