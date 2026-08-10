package observation

import (
	"encoding/json"
	"strings"
)

func errorSummary(payload map[string]any) string {
	if message, ok := payload["error"].(string); ok {
		if summary := failureSummary(message); summary != "" {
			return summary
		}
	}
	if details, ok := payload["error"].(map[string]any); ok {
		return formatErrorDetails(details)
	}
	if result, ok := payload["result"].(map[string]any); ok {
		if message := nestedErrorSummary(result, true); message != "" {
			return message
		}
	}
	return "operation failed"
}

func toolFailure(payload map[string]any) (bool, string) {
	status := strings.ToLower(strings.TrimSpace(stringValue(payload["status"])))
	failedStatus := isErrorStatus(status) || status == "denied" || status == "unauthorized"
	if failedStatus {
		if summary := failureSummary(stringValue(payload["summary"])); summary != "" && !strings.EqualFold(summary, status) {
			return true, summary
		}
		return true, errorSummary(payload)
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return false, ""
	}
	isError, _ := payload["is_error"].(bool)
	if message := nestedErrorSummary(result, isError); message != "" {
		return true, message
	}
	return false, ""
}

func failureActionVerb(tool, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "execute", "command_execute", "command_run":
		return "Command failed"
	case "edit":
		return "Edit failed"
	case "read", "file_read", "source_read", "context_query":
		return "Read failed"
	case "session", "session_open", "session_manage", "session_read", "session_transition":
		return "Session failed"
	case "plan", "plan_create", "plan_manage", "plan_read", "plan_transition":
		return "Plan failed"
	case "artifact", "artifact_manage", "artifact_read", "artifact_register":
		return "Artifact failed"
	case "progress":
		return "Progress failed"
	}
	if value := strings.TrimSpace(tool); value != "" {
		value = strings.ReplaceAll(value, "_", " ")
		return strings.ToUpper(value[:1]) + value[1:] + " failed"
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" && fallback != "Observed" {
		return fallback + " failed"
	}
	return "Operation failed"
}

func nestedErrorSummary(result map[string]any, allowPlainText bool) string {
	rawContent, _ := result["content"].([]any)
	for _, raw := range rawContent {
		item, _ := raw.(map[string]any)
		if item["type"] != "text" {
			continue
		}
		text, _ := item["text"].(string)
		var envelope map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(text)), &envelope) != nil {
			if allowPlainText {
				if message := failureSummary(text); message != "" {
					return message
				}
			}
			continue
		}
		if summary := errorEnvelopeSummary(envelope); summary != "" {
			return summary
		}
		if status, _ := envelope["status"].(string); status == "error" {
			if message, _ := envelope["message"].(string); strings.TrimSpace(message) != "" {
				if summary := failureSummary(message); summary != "" {
					return summary
				}
			}
			return "operation failed"
		}
	}
	return ""
}

func failureSummary(value string) string {
	const maxFailureLines = 3
	lines := make([]string, 0, maxFailureLines)
	for _, raw := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || isFailureHeader(line) {
			continue
		}
		lines = append(lines, compactCodeLine(line))
		if len(lines) == maxFailureLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func isFailureHeader(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "context:", "error:", "errors:", "detail:", "details:", "message:", "cause:":
		return true
	default:
		return false
	}
}

func failureDisplay(value string) string {
	summary := failureSummary(value)
	if summary == "" {
		return ""
	}
	lines := strings.Split(summary, "\n")
	lines[0] = "failed: " + lines[0]
	return strings.Join(lines, "\n")
}

func errorEnvelopeSummary(value map[string]any) string {
	raw, exists := value["error"]
	if !exists || raw == nil {
		return ""
	}
	switch details := raw.(type) {
	case string:
		return failureSummary(details)
	case map[string]any:
		return formatErrorDetails(details)
	default:
		return "operation failed"
	}
}

func formatErrorDetails(details map[string]any) string {
	code, _ := details["code"].(string)
	message, _ := details["message"].(string)
	code = strings.TrimSpace(code)
	message = failureSummary(message)
	switch {
	case code != "" && message != "":
		lines := strings.Split(message, "\n")
		lines[0] = compactCodeLine(code + ": " + lines[0])
		return strings.Join(lines, "\n")
	case message != "":
		return message
	case code != "":
		return compactCodeLine(code)
	default:
		return compactMap(details)
	}
}
