package observation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// TextRenderer groups related observation events into one bounded terminal
// block. It is intentionally stateful because a command's output can arrive
// between its tool.started and tool.completed events.
type TextRenderer struct {
	color       bool
	blocks      map[string]*interactionBlock
	activeKey   string
	fallbackSeq uint64
}

type interactionBlock struct {
	key           string
	sequence      int64
	tool          string
	failed        bool
	continuation  bool
	opened        bool
	closed        bool
	bodyLines     int
	ellipsis      bool
	commandOutput bool
}

// NewTextRenderer creates a text renderer for one observer stream.
func NewTextRenderer(color bool) *TextRenderer {
	return &TextRenderer{color: color, blocks: make(map[string]*interactionBlock)}
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
	key := r.eventKey(event)
	block := r.blocks[key]
	if block == nil || block.closed {
		block = &interactionBlock{
			key:          key,
			sequence:     event.Sequence,
			tool:         event.toolOrType(),
			failed:       eventFailed(event),
			continuation: block != nil && block.closed,
		}
		r.blocks[key] = block
	} else {
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
	if err := renderText(&rendered, event, r.color, block.commandOutput && event.Type == TypeToolCompleted); err != nil {
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
	header := fmt.Sprintf("╭─ #%d · %s", block.sequence, block.tool)
	if block.sequence == 0 {
		header = "╭─ · " + block.tool
	}
	if block.continuation {
		header += " · continued"
	}
	if _, err := fmt.Fprintln(w, paint(header, actionColor(block.tool, block.failed), r.color)); err != nil {
		return err
	}
	block.opened = true
	r.activeKey = block.key
	return nil
}

func (r *TextRenderer) writeBodyLine(w io.Writer, block *interactionBlock, line string) error {
	if strings.TrimSpace(stripANSI(line)) == "" {
		return nil
	}
	if block.bodyLines < maxInteractionBodyLines {
		if _, err := fmt.Fprintln(w, "│ "+line); err != nil {
			return err
		}
		block.bodyLines++
		return nil
	}
	if block.ellipsis {
		return nil
	}
	if _, err := fmt.Fprintln(w, paint("│ ...", ansiYellow, r.color)); err != nil {
		return err
	}
	block.ellipsis = true
	return nil
}

func (r *TextRenderer) close(w io.Writer, block *interactionBlock) error {
	if block == nil || block.closed {
		return nil
	}
	if _, err := fmt.Fprintln(w, paint("╰────────────────────────", actionColor(block.tool, block.failed), r.color)); err != nil {
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
	var payload struct {
		Status string `json:"status"`
	}
	return json.Unmarshal(event.Output, &payload) == nil && payload.Status == "error"
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
