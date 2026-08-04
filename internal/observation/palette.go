package observation

import "strings"

// ColorMode controls ANSI output for terminal text rendering.
type ColorMode uint8

const (
	ColorModeNone ColorMode = iota
	ColorModeANSI16
	ColorModeTrueColor
)

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

	ansiDiffAddedForeground   = "\033[38;2;103;232;160m"
	ansiDiffAddedBackground   = "\033[48;2;24;58;42m"
	ansiDiffRemovedForeground = "\033[38;2;255;143;143m"
	ansiDiffRemovedBackground = "\033[48;2;59;32;37m"
)

const (
	maxInteractionBodyLines = 20
	defaultTerminalWidth    = 120
)

// actionColor assigns a stable color to the semantic operation rather than
// inspecting shell command text. A failed tool call always takes precedence.
func actionColor(tool string, failed bool) string {
	if failed {
		return ansiRed
	}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "command_execute", "command_run":
		return ansiAmber
	case "context_query", "source_read":
		return ansiCyan
	case "file_read":
		return ansiBlue
	case "change_execute", "change_apply", "change_revert", "file.changed":
		return ansiGreen
	case "session_open", "workspace_list", "session.lifecycle":
		return ansiMagenta
	case "progress_report", "observer.notice":
		return ansiGray
	default:
		return ansiGray
	}
}

func diffLineStyle(value string, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	if strings.HasPrefix(value, "+++") {
		return diffHeaderStyle(diffAddedForeground(mode), value)
	}
	if strings.HasPrefix(value, "---") {
		return diffHeaderStyle(diffRemovedForeground(mode), value)
	}
	if strings.HasPrefix(value, "+") {
		return diffContentStyle(value, mode, true)
	}
	if strings.HasPrefix(value, "-") {
		return diffContentStyle(value, mode, false)
	}
	return value
}

// formatDiffLine keeps the width parameter for the renderer call site, but
// deliberately does not fill the rest of the terminal with background color.
// A blank added/deleted line receives only its marker color, avoiding large
// blocks that visually disconnect adjacent Diff lines.
func formatDiffLine(value string, mode ColorMode, _ int) string {
	return diffLineStyle(value, mode)
}

func isDiffContentLine(value string) bool {
	return (strings.HasPrefix(value, "+") && !strings.HasPrefix(value, "+++")) ||
		(strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "---"))
}

func diffContentStyle(value string, mode ColorMode, added bool) string {
	if mode == ColorModeNone {
		return value
	}
	foreground := ansiRed
	background := ""
	if added {
		foreground = ansiGreen
	}
	if mode == ColorModeTrueColor {
		if added {
			foreground = ansiDiffAddedForeground
			background = ansiDiffAddedBackground
		} else {
			foreground = ansiDiffRemovedForeground
			background = ansiDiffRemovedBackground
		}
		// A line containing only "+" or "-" represents a blank source line.
		// Keep the marker visible without turning the whole row into a block.
		if strings.TrimSpace(value[1:]) == "" {
			background = ""
		}
	}
	return background + foreground + value + ansiReset
}

func diffAddedForeground(mode ColorMode) string {
	if mode == ColorModeTrueColor {
		return ansiDiffAddedForeground
	}
	return ansiGreen
}

func diffRemovedForeground(mode ColorMode) string {
	if mode == ColorModeTrueColor {
		return ansiDiffRemovedForeground
	}
	return ansiRed
}

func diffHeaderStyle(foreground, value string) string {
	return foreground + value + ansiReset
}

func (event Event) toolOrType() string {
	if strings.TrimSpace(event.Tool) != "" {
		return event.Tool
	}
	return event.Type
}
