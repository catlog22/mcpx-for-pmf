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
	lines := visibleDiffLines(document)
	if len(lines) == 0 {
		return false, nil
	}
	for index, line := range lines {
		if limit > 0 && index >= limit {
			return true, nil
		}
		if line.kind == diffLineMetadata {
			if err := writeCodeChild(w, "    | ...", options, options.terminalWidth-8); err != nil {
				return false, err
			}
			continue
		}
		prefix := diffLinePrefix(line)
		value := compactCodeLine(sanitizeTerminalText(prefix + line.text))
		value = styleRenderedDiffLine(value, line.kind, options.colorMode)
		if width := options.terminalWidth - 8; width > 0 {
			value = truncateRenderedLine(value, width)
		}
		if _, err := fmt.Fprintf(w, "    %s\n", value); err != nil {
			return false, err
		}
	}
	return false, nil
}

// visibleDiffLines removes transport-only unified-diff headers and keeps at
// most five context lines around each changed line. The terminal already
// prints the file path and change counts in the action row, so repeating
// ---/+++/@@ makes human observation harder to scan without adding meaning.

// visibleDiffLines removes transport-only unified-diff headers and keeps at
// most five context lines around each changed line. The terminal already
// prints the file path and change counts in the action row, so repeating
// ---/+++/@@ makes human observation harder to scan without adding meaning.
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
