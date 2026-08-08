package observation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// displayWidth returns the terminal cell width of a rendered line, ignoring
// ANSI color sequences and accounting for wide and combining Unicode runes.
func displayWidth(value string) int {
	width := 0
	for _, r := range strings.TrimSuffix(stripANSI(value), "\n") {
		width += runeDisplayWidth(r)
	}
	return width
}

// truncateRenderedLine keeps ANSI sequences while shortening a line to its
// terminal cell budget. The ellipsis is included in that budget.
func truncateRenderedLine(value string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displayWidth(value) <= maxWidth {
		return value
	}
	if maxWidth <= 3 {
		prefix := takeRenderedWidth(value, maxWidth)
		if strings.Contains(value, "\033") {
			return prefix + ansiReset
		}
		return prefix
	}
	prefix := takeRenderedWidth(value, maxWidth-3)
	if strings.Contains(prefix, "\033") || strings.Contains(value, "\033") {
		return prefix + "..." + ansiReset
	}
	return prefix + "..."
}

// wrapRenderedLine keeps the complete rendered value while fitting each
// segment within the terminal cell budget. ANSI styles active at a wrap point
// are reopened on the continuation line so colored output remains valid.
func wrapRenderedLine(value string, maxWidth int) []string {
	if value == "" {
		return []string{""}
	}
	if maxWidth <= 0 || displayWidth(value) <= maxWidth {
		return []string{value}
	}

	segments := make([]string, 0, 2)
	var current strings.Builder
	activeANSI := make([]string, 0, 2)
	width := 0
	flush := func() {
		if current.Len() == 0 {
			return
		}
		segments = append(segments, current.String())
		current.Reset()
		for _, sequence := range activeANSI {
			current.WriteString(sequence)
		}
		width = 0
	}

	for index := 0; index < len(value); {
		if value[index] == '\033' {
			sequence, next := readANSISequence(value, index)
			if next == index {
				sequence = string(value[index])
				next++
			}
			current.WriteString(sequence)
			if strings.HasSuffix(sequence, "0m") {
				activeANSI = activeANSI[:0]
			} else if strings.HasSuffix(sequence, "m") {
				activeANSI = append(activeANSI, sequence)
			}
			index = next
			continue
		}

		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if size == 0 {
			break
		}
		runeWidth := runeDisplayWidth(runeValue)
		if width > 0 && width+runeWidth > maxWidth {
			if len(activeANSI) > 0 {
				current.WriteString(ansiReset)
			}
			flush()
		}
		current.WriteRune(runeValue)
		width += runeWidth
		index += size
	}
	flush()
	if len(segments) == 0 {
		return []string{value}
	}
	return segments
}

func readANSISequence(value string, start int) (string, int) {
	index := start + 1
	if index < len(value) && value[index] == '[' {
		index++
	}
	for index < len(value) {
		current := value[index]
		index++
		if current >= '@' && current <= '~' {
			return value[start:index], index
		}
	}
	return value[start:], len(value)
}

func takeRenderedWidth(value string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	var builder strings.Builder
	width := 0
	for index := 0; index < len(value); {
		if value[index] == '\033' {
			start := index
			index++
			if index < len(value) && value[index] == '[' {
				index++
			}
			for index < len(value) {
				current := value[index]
				index++
				if current >= '@' && current <= '~' {
					break
				}
			}
			builder.WriteString(value[start:index])
			continue
		}

		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if size == 0 {
			break
		}
		runeWidth := runeDisplayWidth(runeValue)
		if width+runeWidth > maxWidth {
			break
		}
		builder.WriteRune(runeValue)
		width += runeWidth
		index += size
	}
	return builder.String()
}

func runeDisplayWidth(value rune) int {
	if value == '\t' {
		return 4
	}
	if unicode.IsControl(value) || unicode.Is(unicode.Mn, value) || unicode.Is(unicode.Me, value) {
		return 0
	}
	if isWideRune(value) {
		return 2
	}
	return 1
}

func isWideRune(value rune) bool {
	return value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0x303e) ||
		(value >= 0x3040 && value <= 0x3247) ||
		(value >= 0x3250 && value <= 0x4dbf) ||
		(value >= 0x4e00 && value <= 0xa4c6) ||
		(value >= 0xa960 && value <= 0xa97c) ||
		(value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) ||
		(value >= 0xfe10 && value <= 0xfe19) ||
		(value >= 0xfe30 && value <= 0xfe6b) ||
		(value >= 0xff01 && value <= 0xff60) ||
		(value >= 0xffe0 && value <= 0xffe6) ||
		(value >= 0x1f300 && value <= 0x1faff))
}
