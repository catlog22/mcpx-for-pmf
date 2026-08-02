package observation

import (
	"bytes"
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
	blocks                  map[string]*interactionBlock
	activeKey               string
	fallbackSeq             uint64
	lastProgressFingerprint string
}

type interactionBlock struct {
	key             string
	sequence        int64
	remoteSessionID string
	tool            string
	failed          bool
	continuation    bool
	opened          bool
	closed          bool
	bodyLines       int
	pendingLine     string
	ellipsis        bool
	commandOutput   bool
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
	return &TextRenderer{colorMode: mode, width: terminalWidth, blocks: make(map[string]*interactionBlock)}
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
	if event.Tool == "progress_report" && event.Type == TypeToolCompleted {
		fingerprint := progressFingerprint(event)
		if fingerprint != "" && fingerprint == r.lastProgressFingerprint {
			return nil
		}
		r.lastProgressFingerprint = fingerprint
	} else if event.Tool != "progress_report" || event.Type != TypeToolStarted {
		r.lastProgressFingerprint = ""
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
			continuation:    block != nil && block.closed,
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
	}

	if event.Type == TypeToolStarted {
		if event.Tool != "" {
			block.tool = event.Tool
		}
		return nil
	}
	if event.Type == TypeCommandOutput {
		block.commandOutput = true
	}

	var rendered bytes.Buffer
	if err := renderTextWithOptions(&rendered, event, renderOptions{colorMode: r.colorMode, terminalWidth: r.width}, block.commandOutput && event.Type == TypeToolCompleted); err != nil {
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
	if _, err := fmt.Fprintln(w, paint(header, actionColor(block.tool, block.failed), r.colorMode != ColorModeNone)); err != nil {
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
	if _, err := fmt.Fprintln(w, paint(footer, actionColor(block.tool, block.failed), r.colorMode != ColorModeNone)); err != nil {
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
