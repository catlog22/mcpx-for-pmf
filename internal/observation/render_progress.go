package observation

import (
	"fmt"
	"io"
	"strings"
)

func renderProgressSummary(w io.Writer, event Event, options renderOptions) error {
	return writeChildren(w, semanticContextLines(event, options.detail), options.colorMode)
}

type progressView struct {
	Status      string
	Current     string
	Result      string
	Next        string
	Phase       string
	RelatedTool string
}

func isProgressTool(tool string) bool {
	return strings.EqualFold(strings.TrimSpace(tool), "progress")
}

func progressEventView(event Event) progressView {
	input := inputMap(event.Input)
	view := progressView{
		Status:      strings.ToLower(firstProgressString(input, "status")),
		Current:     firstProgressString(input, "current"),
		Result:      firstProgressString(input, "result"),
		Next:        firstProgressString(input, "next"),
		Phase:       firstProgressString(input, "phase"),
		RelatedTool: firstProgressString(input, "related_tool"),
	}
	if view.Status == "" {
		view.Status = "in_progress"
	}
	if view.Current == "" {
		view.Current = strings.TrimSpace(event.ProgressSummary)
	}
	return view
}

func firstProgressString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(input[key]); value != "" {
			return value
		}
	}
	return ""
}

func renderProgressCompleted(w io.Writer, event Event, options renderOptions) error {
	view := progressEventView(event)
	if view.Current == "" {
		view.Current = "progress update"
	}
	marker, verb, color := progressAppearance(view.Status)
	if err := writeProgressAction(w, marker, verb, view.Current, color, options.colorMode); err != nil {
		return err
	}
	details := make([]string, 0, 4)
	if view.Result != "" {
		details = append(details, "result: "+view.Result)
	}
	if view.Next != "" {
		details = append(details, "next: "+view.Next)
	}
	if options.detail && view.Phase != "" {
		details = append(details, "phase: "+view.Phase)
	}
	if options.detail && view.RelatedTool != "" {
		details = append(details, "related tool: "+view.RelatedTool)
	}
	return writeChildren(w, details, options.colorMode)
}

func progressAppearance(status string) (marker, verb, color string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return "✓", "Done", ansiGreen
	case "waiting_for_user":
		return "?", "Waiting", ansiYellow
	case "blocked":
		return "!", "Blocked", ansiYellow
	case "failed":
		return "✗", "Failed", ansiRed
	default:
		return "◆", "Progress", ansiCyan
	}
}

func writeProgressAction(w io.Writer, marker, verb, current, color string, mode ColorMode) error {
	marker = sanitizeTerminalText(marker)
	verb = sanitizeTerminalText(verb)
	current = strings.TrimSpace(sanitizeTerminalText(current))
	if current == "" {
		current = "progress update"
	}
	enabled := mode != ColorModeNone
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(marker, color, enabled), paint(verb, color, enabled), current)
	return err
}
