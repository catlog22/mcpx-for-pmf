package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// RenderText writes a terminal-style timeline item. Tool-start events are
// intentionally silent: the corresponding completion event renders one
// past-tense action with a compact result, so replayed history does not show
// TOOL STARTED/COMPLETED protocol labels or duplicate actions.
func RenderText(w io.Writer, event Event, color bool) error {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode, diffMode: DiffModeFull}, false)
}

func renderText(w io.Writer, event Event, color, suppressCommandOutput bool) error {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode, diffMode: DiffModeFull}, suppressCommandOutput)
}

type renderOptions struct {
	colorMode            ColorMode
	terminalWidth        int
	detail               bool
	diffMode             DiffMode
	diffCache            *diffDocumentCache
	suppressAction       bool
	suppressOutputAction bool
	commandOutputStarted bool
	suppressContext      bool
	suppressDuration     bool
	outputLineStart      int
}

func renderTextWithOptions(w io.Writer, event Event, options renderOptions, suppressCommandOutput bool) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	switch event.Type {
	case TypeToolStarted:
		if !hasSemanticContext(event) {
			return nil
		}
		return renderProgressSummary(w, event, options)
	case TypeToolCompleted:
		return renderToolCompleted(w, event, options, suppressCommandOutput)
	case TypeCommandOutput:
		return renderCommandOutput(w, event, options)
	case TypeFileChanged:
		return renderFileChanged(w, event, options)
	case TypeSessionLifecycle:
		return renderSummaryEvent(w, event, lifecycleVerb(event.Summary), event.Summary, event.Output, options.colorMode)
	case TypeObserverNotice:
		return renderSummaryEvent(w, event, "Observed", event.Summary, event.Output, options.colorMode)
	case TypeAgentActivity:
		return renderAgentActivity(w, event, options)
	default:
		summary := event.Summary
		if strings.TrimSpace(summary) == "" {
			summary = event.Type
		}
		return renderSummaryEvent(w, event, "Observed", summary, event.Output, options.colorMode)
	}
}

// RenderJSON writes one complete JSON event per line for scripts and log
// ingestion. It intentionally does not wrap the event in a protocol frame.
func RenderJSON(w io.Writer, event Event) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	event.SetDefaults()
	return json.NewEncoder(w).Encode(event)
}

func renderSummaryEvent(w io.Writer, event Event, verb, summary string, raw []byte, mode ColorMode) error {
	if strings.TrimSpace(summary) == "" {
		summary = "event"
	}
	if err := writeEventAction(w, event, verb, summary, actionColor(event.toolOrType(), false, mode), mode); err != nil {
		return err
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil {
		if details := summaryEventOutput(event, value); details != "" {
			return writeChildren(w, []string{details}, mode)
		}
	}
	return nil
}

func summaryEventOutput(event Event, value map[string]any) string {
	if event.Type != TypeObserverNotice && event.Type != TypeSessionLifecycle {
		return compactMap(value)
	}
	// Remote events already carry their useful information in the action title
	// (for example, "command.started: go test ./..."). Their source sequence
	// and nested metadata are transport details, not a useful terminal summary.
	metadata, _ := value["metadata"].(map[string]any)
	if len(metadata) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"plan_task_id", "execution_task_id", "status", "exit_code"} {
		if text := strings.TrimSpace(formatNumber(metadata[key])); text != "" {
			parts = append(parts, key+"="+text)
		}
	}
	return strings.Join(parts, " ")
}

func lifecycleVerb(summary string) string {
	value := strings.ToLower(summary)
	switch {
	case strings.Contains(value, "created"), strings.Contains(value, "opened"):
		return "Opened"
	case strings.Contains(value, "closed"):
		return "Closed"
	case strings.Contains(value, "updated"):
		return "Updated"
	default:
		return "Observed"
	}
}

func publicView(raw []byte) string {
	input := inputMap(raw)
	view, _ := input["view"].(string)
	return strings.ToLower(strings.TrimSpace(view))
}

func eventFactLine(event Event, detail, suppressDuration bool) string {
	parts := make([]string, 0, 10)
	command := isCommandTool(event.Tool)
	if tool := strings.TrimSpace(event.Tool); tool != "" && detail {
		parts = append(parts, "tool="+tool)
	}
	if detail && event.Command != "" {
		parts = append(parts, "command="+compactCommand(event.Command))
	}
	if detail && event.WorkingDirectory != "" {
		parts = append(parts, "cwd="+compactLine(event.WorkingDirectory))
	}
	if event.ExitCode != nil {
		if command && !detail {
			parts = append(parts, fmt.Sprintf("exit %d", *event.ExitCode))
		} else {
			parts = append(parts, fmt.Sprintf("exit=%d", *event.ExitCode))
		}
	}
	if event.DurationMs > 0 {
		duration := (time.Duration(event.DurationMs) * time.Millisecond).String()
		switch {
		case !detail:
			parts = append(parts, "time "+duration)
		case !suppressDuration:
			parts = append(parts, fmt.Sprintf("duration=%dms", event.DurationMs))
		}
	}
	if event.SkillName != "" {
		parts = append(parts, "skill="+event.SkillName)
	}
	if event.MCPServer != "" {
		mcp := "mcp=" + event.MCPServer
		if event.MCPTool != "" {
			mcp += "/" + event.MCPTool
		}
		parts = append(parts, mcp)
	}
	if detail && event.Path != "" {
		parts = append(parts, "path="+event.Path)
	}
	if detail && event.Phase != "" {
		parts = append(parts, "phase="+event.Phase)
	}
	if detail && event.CallID != "" {
		parts = append(parts, "call="+event.CallID)
	}
	if detail && event.OperationID != "" {
		parts = append(parts, "operation="+event.OperationID)
	}
	if detail && event.ParentOperationID != "" {
		parts = append(parts, "parent_operation="+event.ParentOperationID)
	}
	if detail && event.StepID != "" {
		parts = append(parts, "step="+event.StepID)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
