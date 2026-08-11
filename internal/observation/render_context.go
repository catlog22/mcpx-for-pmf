package observation

import (
	"io"
	"strings"
)

func hasSemanticContext(event Event) bool {
	return strings.TrimSpace(event.Purpose) != "" || strings.TrimSpace(event.ReasoningSummary) != "" ||
		strings.TrimSpace(event.ProgressSummary) != "" || strings.TrimSpace(event.NextStep) != "" ||
		strings.TrimSpace(event.PlanID) != "" || strings.TrimSpace(event.PlanTaskID) != "" ||
		strings.TrimSpace(event.ExecutionTaskID) != ""
}

func renderPurposeBanner(w io.Writer, event Event, mode ColorMode) error {
	purpose := strings.TrimSpace(event.Purpose)
	if purpose == "" {
		return nil
	}
	return renderSemanticBanner(w, "Purpose", purpose, eventClock(event), mode)
}

func semanticContextLines(event Event, detail bool) []string {
	groups := []semanticContextGroup{
		{
			{label: "progress", value: event.ProgressSummary},
			{label: "next", value: event.NextStep},
		},
		{
			{label: "reasoning", value: event.ReasoningSummary},
			{label: "plan", value: event.PlanID},
			{label: "plan task", value: event.PlanTaskID},
			{label: "execution task", value: event.ExecutionTaskID},
		},
	}
	if detail {
		groups[1] = append(groups[1], semanticContextField{label: "operation", value: event.OperationID})
	}
	return semanticContextGroups(groups)
}

type semanticContextField struct {
	label string
	value string
}

type semanticContextGroup []semanticContextField

func semanticContextGroups(groups []semanticContextGroup) []string {
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		parts := make([]string, 0, len(group))
		for _, field := range group {
			if value := compactLine(field.value); value != "" {
				parts = append(parts, field.label+": "+value)
			}
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " · "))
		}
	}
	return lines
}
