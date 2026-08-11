package observation

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func renderAgentActivity(w io.Writer, event Event, options renderOptions) error {
	kind := agentActivityKindLabel(event.ActivityKind)
	summary := strings.TrimSpace(event.ProgressSummary)
	if summary == "" {
		summary = strings.TrimSpace(event.Summary)
	}
	if summary == "" {
		summary = "activity update"
	}
	kind = sanitizeTerminalText(kind)
	summary = sanitizeTerminalText(summary)
	color := options.colorMode != ColorModeNone
	semanticColor := pickColor(ansiMagenta, ansiTrueMagenta, options.colorMode)
	separator := paint("──", semanticColor, color)
	clock := eventClock(event)
	if clock == "" {
		if _, err := fmt.Fprintf(w, "%s %s %s %s %s\n", paint("◇", semanticColor, color), paint(kind, semanticColor, color), separator, summary, separator); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(w, "%s %s %s %s %s %s %s\n", paint("◇", semanticColor, color), paint(kind, semanticColor, color), separator, summary, separator, paint(clock, semanticColor, color), separator); err != nil {
		return err
	}
	if !options.detail {
		return nil
	}
	parts := make([]string, 0, 4)
	if state := strings.TrimSpace(event.Status); state != "" {
		parts = append(parts, "state="+state)
	}
	if turnID := strings.TrimSpace(event.TurnID); turnID != "" {
		parts = append(parts, "turn="+turnID)
	}
	if event.ActivitySequence > 0 {
		parts = append(parts, "seq="+strconv.FormatInt(event.ActivitySequence, 10))
	}
	if callID := strings.TrimSpace(event.RelatedCallID); callID != "" {
		parts = append(parts, "call="+callID)
	}
	if len(parts) == 0 {
		return nil
	}
	return writeChildren(w, []string{strings.Join(parts, " · ")}, options.colorMode)
}

func agentActivityKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "intent":
		return "Intent"
	case "hypothesis":
		return "Hypothesis"
	case "evidence":
		return "Evidence"
	case "conclusion":
		return "Conclusion"
	case "next":
		return "Next"
	case "status":
		return "Status"
	default:
		return "Activity"
	}
}
