package observation

import (
	"fmt"
	"io"
	"strings"
)

func diffStats(diff string) string {
	return parseDiffDocument(diff).stats()
}

func renderFullDiffWithContext(w io.Writer, diff string, options renderOptions) error {
	_, err := renderDiffDocument(w, options.diffCache.get(diff), options, 0)
	return err
}

func renderDiffDocument(w io.Writer, document diffDocument, options renderOptions, limit int) (bool, error) {
	lines := fullDiffLines(document)
	if options.diffMode != DiffModeFull {
		lines = visibleDiffLines(document)
	}
	if len(lines) == 0 {
		return false, nil
	}
	for index, line := range lines {
		if limit > 0 && index >= limit {
			return true, nil
		}
		if line.kind == diffLineMetadata && options.diffMode != DiffModeFull {
			if err := writeCodeChild(w, "    | ...", options, options.terminalWidth-8); err != nil {
				return false, err
			}
			continue
		}
		prefix := diffLinePrefix(line)
		value := sanitizeTerminalText(prefix + line.text)
		if options.diffMode != DiffModeFull {
			value = compactCodeLine(value)
		}
		value = styleRenderedDiffLine(value, line.kind, options.colorMode)
		rendered := "    " + value
		if options.diffMode != DiffModeFull {
			rendered = truncateDiffLine(rendered, options.terminalWidth)
		}
		if _, err := fmt.Fprintln(w, rendered); err != nil {
			return false, err
		}
	}
	return false, nil
}

// truncateDiffLine applies the same width budget that timeline.flushBodyLine
// uses when it wraps the final rendered line. Keeping the fixed indentation in
// the value being truncated prevents a trailing ellipsis from being split onto
// a continuation line.
func truncateDiffLine(value string, terminalWidth int) string {
	if terminalWidth <= 0 {
		return value
	}
	continuationIndent := strings.Repeat(" ", leadingSpaceWidth(value)+2)
	bodyWidth := terminalWidth - displayWidth(continuationIndent)
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	return truncateRenderedLine(value, bodyWidth)
}

// fullDiffLines keeps every source-content line while omitting unified-diff
// transport headers already represented by the action row and line numbers.
func fullDiffLines(document diffDocument) []diffLine {
	visible := make([]diffLine, 0, len(document.lines))
	for _, line := range document.lines {
		switch line.kind {
		case diffLineFileHeader, diffLineHunkHeader, diffLineMetadata:
			continue
		default:
			visible = append(visible, line)
		}
	}
	return visible
}

// visibleDiffLines is the compact preview projection: it removes transport-only
// unified-diff headers and keeps at most five context lines around each change.
func visibleDiffLines(document diffDocument) []diffLine {
	if len(document.lines) == 0 {
		return nil
	}
	keep := make([]bool, len(document.lines))
	for index, line := range document.lines {
		if line.kind != diffLineAdded && line.kind != diffLineRemoved {
			continue
		}
		start := index - 5
		if start < 0 {
			start = 0
		}
		end := index + 5
		if end >= len(document.lines) {
			end = len(document.lines) - 1
		}
		for cursor := start; cursor <= end; cursor++ {
			if document.lines[cursor].kind == diffLineContext || document.lines[cursor].kind == diffLineAdded || document.lines[cursor].kind == diffLineRemoved || document.lines[cursor].kind == diffLineNoNewline {
				keep[cursor] = true
			}
		}
	}
	visible := make([]diffLine, 0, len(document.lines))
	gap := false
	for index, line := range document.lines {
		if line.kind == diffLineFileHeader || line.kind == diffLineHunkHeader || line.kind == diffLineMetadata {
			continue
		}
		if !keep[index] {
			if len(visible) > 0 {
				gap = true
			}
			continue
		}
		if gap {
			visible = append(visible, diffLine{kind: diffLineMetadata})
			gap = false
		}
		visible = append(visible, line)
	}
	return visible
}

func diffLinePrefix(line diffLine) string {
	switch {
	case line.hasNew:
		return fmt.Sprintf("%3d | ", line.newLine)
	case line.hasOld:
		return fmt.Sprintf("%3d | ", line.oldLine)
	default:
		return "| "
	}
}

func compactLine(value string) string {
	value = strings.TrimSpace(strings.SplitN(value, "\n", 2)[0])
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxToolSummaryRunes {
		return string(runes[:maxToolSummaryRunes-3]) + "..."
	}
	return value
}

func compactCodeLine(value string) string {
	value = strings.TrimRight(value, "\r")
	runes := []rune(value)
	if len(runes) > maxToolSummaryRunes {
		return string(runes[:maxToolSummaryRunes-3]) + "..."
	}
	return value
}
