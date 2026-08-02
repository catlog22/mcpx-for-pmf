package observation

import "strings"

const (
	ansiReset   = "\033[0m"
	ansiAmber   = "\033[33m"
	ansiYellow  = "\033[33m"
	ansiCyan    = "\033[36m"
	ansiBlue    = "\033[34m"
	ansiGreen   = "\033[32m"
	ansiMagenta = "\033[35m"
	ansiRed     = "\033[31m"
	ansiGray    = "\033[90m"
)

const (
	maxInteractionLines     = 10
	maxInteractionBodyLines = 7
)

// actionColor assigns a stable color to the semantic operation rather than
// inspecting shell command text. A failed tool call always takes precedence.
func actionColor(tool string, failed bool) string {
	if failed {
		return ansiRed
	}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "command_execute":
		return ansiAmber
	case "context_query":
		return ansiCyan
	case "file_read":
		return ansiBlue
	case "change_execute", "file.changed":
		return ansiGreen
	case "session_open", "workspace_list", "session.lifecycle":
		return ansiMagenta
	case "progress_report", "observer.notice":
		return ansiGray
	default:
		return ansiGray
	}
}

func (event Event) toolOrType() string {
	if strings.TrimSpace(event.Tool) != "" {
		return event.Tool
	}
	return event.Type
}
