package observation

import (
	"encoding/json"
	"strings"
)

// ColorMode controls ANSI output for terminal text rendering.
type ColorMode uint8

const (
	ColorModeNone ColorMode = iota
	ColorModeANSI16
	ColorModeTrueColor
)

const (
	ansiReset     = "\033[0m"
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiUnderline = "\033[4m"
	ansiAmber     = "\033[33m"
	ansiYellow    = "\033[33m"
	ansiCyan      = "\033[36m"
	ansiBlue      = "\033[34m"
	ansiGreen     = "\033[32m"
	ansiMagenta   = "\033[35m"
	ansiRed       = "\033[31m"
	ansiGray      = "\033[90m"

	ansiDiffAddedForeground   = "\033[38;2;103;232;160m"
	ansiDiffAddedBackground   = "\033[48;2;24;58;42m"
	ansiDiffRemovedForeground = "\033[38;2;255;143;143m"
	ansiDiffRemovedBackground = "\033[48;2;59;32;37m"
	ansiDiffHunkForeground    = "\033[38;2;121;192;255m"

	// True-color semantic palette used when ColorModeTrueColor is active. It
	// keeps the same tool/status meaning as the ANSI16 palette with finer,
	// lower-emission colors for dark terminals.
	ansiTrueMuted   = "\033[38;2;148;163;184m" // slate-400: secondary labels
	ansiTrueCyan    = "\033[38;2;34;211;238m"  // cyan-400: reads/searches
	ansiTrueBlue    = "\033[38;2;96;165;250m"  // blue-400: files/artifacts/running
	ansiTrueGreen   = "\033[38;2;52;211;153m"  // emerald-400: edits/changes
	ansiTrueAmber   = "\033[38;2;251;191;36m"  // amber-400: command execution
	ansiTrueYellow  = "\033[38;2;253;224;71m"  // yellow-300: plans/confirmation
	ansiTrueMagenta = "\033[38;2;232;121;249m" // fuchsia-400: sessions/mcp
	ansiTrueRed     = "\033[38;2;248;113;113m" // red-400: failures
)

const (
	maxInteractionBodyLines = 50
	defaultTerminalWidth    = 120
)

// pickColor resolves an ANSI16 code or its true-color counterpart based on the
// active color mode. ColorModeNone callers never reach this path.
func pickColor(ansi16, trueColor string, mode ColorMode) string {
	if mode == ColorModeTrueColor {
		return trueColor
	}
	return ansi16
}

// actionColor assigns a stable color to the semantic operation rather than
// inspecting shell command text. A failed tool call always takes precedence.
func actionColor(tool string, failed bool, mode ColorMode) string {
	if failed {
		return pickColor(ansiRed, ansiTrueRed, mode)
	}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "execute", "command_execute", "command_run":
		return pickColor(ansiAmber, ansiTrueAmber, mode)
	case "read", "context_query", "source_read", "skill_tool", "observe", "runtime_read", "progress":
		return pickColor(ansiCyan, ansiTrueCyan, mode)
	case "file_read", "artifact":
		return pickColor(ansiBlue, ansiTrueBlue, mode)
	case "edit", "file.changed":
		return pickColor(ansiGreen, ansiTrueGreen, mode)
	case "plan":
		return pickColor(ansiYellow, ansiTrueYellow, mode)
	case "session", "session_open", "workspace_list", "session.lifecycle", "mcp_tool":
		return pickColor(ansiMagenta, ansiTrueMagenta, mode)
	case "observer.notice":
		return pickColor(ansiGray, ansiTrueMuted, mode)
	default:
		return pickColor(ansiGray, ansiTrueMuted, mode)
	}
}

func eventStatus(event Event) string {
	status := strings.ToLower(strings.TrimSpace(event.Status))
	if eventFailed(event) {
		return "failed"
	}
	if status == "" && event.Type == TypeToolCompleted {
		var payload struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(event.Output, &payload) == nil {
			status = strings.ToLower(strings.TrimSpace(payload.Status))
		}
	}
	return status
}

func eventActionColor(event Event, fallback string, mode ColorMode) string {
	switch eventStatus(event) {
	case "failed", "error", "cancelled", "canceled", "interrupted":
		return pickColor(ansiRed, ansiTrueRed, mode)
	case "need_confirmation", "needs_confirmation", "waiting_confirmation", "blocked":
		return pickColor(ansiYellow, ansiTrueYellow, mode)
	case "queued", "running", "accepted", "in_progress":
		return pickColor(ansiBlue, ansiTrueBlue, mode)
	case "succeeded", "success", "ok", "applied":
		return fallback
	default:
		if fallback != "" {
			return fallback
		}
		return actionColor(event.toolOrType(), false, mode)
	}
}

// mutedColor returns the secondary-text color for the active mode. It is used
// for fact labels and command line numbers.
func mutedColor(mode ColorMode) string {
	return pickColor(ansiGray, ansiTrueMuted, mode)
}

func eventMarker(event Event) string {
	switch eventStatus(event) {
	case "failed", "error", "cancelled", "canceled", "interrupted":
		return "!"
	case "need_confirmation", "needs_confirmation", "waiting_confirmation":
		return "?"
	case "blocked":
		return "x"
	case "queued", "running", "accepted", "in_progress":
		return "~"
	default:
		return "•"
	}
}

func diffLineStyle(value string, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	if strings.HasPrefix(value, "+++") {
		return styleDiffLine(value, diffLineFileHeader, mode)
	}
	if strings.HasPrefix(value, "---") {
		return styleDiffLine(value, diffLineFileHeader, mode)
	}
	if strings.HasPrefix(value, "+") {
		return styleDiffLine(value, diffLineAdded, mode)
	}
	if strings.HasPrefix(value, "-") {
		return styleDiffLine(value, diffLineRemoved, mode)
	}
	if strings.HasPrefix(value, "@@") {
		return styleDiffLine(value, diffLineHunkHeader, mode)
	}
	if strings.HasPrefix(value, `\ No newline at end of file`) {
		return styleDiffLine(value, diffLineNoNewline, mode)
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

func styleDiffLine(value, kind string, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	switch kind {
	case diffLineAdded:
		return diffContentStyle(value, mode, true)
	case diffLineRemoved:
		return diffContentStyle(value, mode, false)
	case diffLineFileHeader:
		return ansiBold + diffHeaderForeground(value, mode) + value + ansiReset
	case diffLineHunkHeader:
		foreground := ansiCyan
		if mode == ColorModeTrueColor {
			foreground = ansiDiffHunkForeground
		}
		return ansiBold + foreground + value + ansiReset
	case diffLineNoNewline:
		return ansiUnderline + ansiYellow + value + ansiReset
	case diffLineContext:
		return ansiDim + value + ansiReset
	default:
		return ansiDim + value + ansiReset
	}
}

func styleRenderedDiffLine(value, kind string, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	if kind == diffLineFileHeader {
		header := value
		if separator := strings.Index(value, "| "); separator >= 0 {
			header = value[separator+2:]
		}
		return ansiBold + diffHeaderForeground(header, mode) + value + ansiReset
	}
	if kind != diffLineAdded && kind != diffLineRemoved {
		return styleDiffLine(value, kind, mode)
	}
	if separator := strings.Index(value, "| "); separator >= 0 {
		prefixEnd := separator + 2
		return value[:prefixEnd] + styleDiffLine(value[prefixEnd:], kind, mode)
	}
	return styleDiffLine(value, kind, mode)
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

func diffHeaderForeground(value string, mode ColorMode) string {
	if strings.HasPrefix(value, "+++") {
		return diffAddedForeground(mode)
	}
	return diffRemovedForeground(mode)
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
