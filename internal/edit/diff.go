package edit

import (
	"fmt"
	"strings"
)

// UnifiedDiff builds a simple unified diff for one path and returns the number
// of changed lines (insertions + deletions, excluding headers and context).
func UnifiedDiff(path, oldText, newText string) (diff string, changed int) {
	oldText = normalizeDiffNewlines(oldText)
	newText = normalizeDiffNewlines(newText)
	oldFinalNewline := hasFinalNewline(oldText)
	newFinalNewline := hasFinalNewline(newText)
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	if equalStringSlices(oldLines, newLines) {
		if oldFinalNewline == newFinalNewline {
			return "", 0
		}
		// A final-newline-only edit has no different logical line text. Show the
		// affected last line as a replacement so the result is still auditable.
		ops := make([]diffOp, 0, len(oldLines)+1)
		if len(oldLines) > 0 {
			for index := 0; index < len(oldLines)-1; index++ {
				ops = append(ops, diffOp{kind: ' ', line: oldLines[index]})
			}
			ops = append(ops, diffOp{kind: '-', line: oldLines[len(oldLines)-1]})
			ops = append(ops, diffOp{kind: '+', line: newLines[len(newLines)-1]})
		} else {
			return "", 0
		}
		diff, changed = renderDiff(path, oldLines, newLines, ops)
		return diff + "\\ No newline at end of file\n", changed
	}

	// The actual change count is calculated from the reconstructed diff below.
	ops := lineDiff(oldLines, newLines)

	return renderDiffWithFinalNewline(path, oldLines, newLines, ops, oldFinalNewline, newFinalNewline)
}

func renderDiffWithFinalNewline(path string, oldLines, newLines []string, ops []diffOp, oldFinalNewline, newFinalNewline bool) (string, int) {
	diff, changed := renderDiff(path, oldLines, newLines, ops)
	if oldFinalNewline != newFinalNewline {
		diff += "\\ No newline at end of file\n"
	}
	return diff, changed
}

func renderDiff(path string, oldLines, newLines []string, ops []diffOp) (string, int) {
	var b strings.Builder
	changed := 0
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	oldStart, newStart := 1, 1
	if len(oldLines) == 0 {
		oldStart = 0
	}
	if len(newLines) == 0 {
		newStart = 0
	}
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, len(oldLines), newStart, len(newLines))

	for _, op := range ops {
		switch op.kind {
		case ' ':
			b.WriteByte(' ')
			b.WriteString(op.line)
			b.WriteByte('\n')
		case '-':
			b.WriteByte('-')
			b.WriteString(op.line)
			b.WriteByte('\n')
			changed++
		case '+':
			b.WriteByte('+')
			b.WriteString(op.line)
			b.WriteByte('\n')
			changed++
		}
	}
	return b.String(), changed
}

// CountChangedLines counts strict unified-diff change lines in an existing diff.
func CountChangedLines(diff string) int {
	changed := 0
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			changed++
		}
	}
	return changed
}

type diffOp struct {
	kind byte // ' ', '+', '-'
	line string
}

func lineDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// Myers keeps memory proportional to the edit frontier instead of the
	// product of the two file sizes. This matters for a large file with a
	// small replacement, which is the common edit-tool workload.
	max := n + m
	v := map[int]int{1: 0}
	trace := make([]map[int]int, 0, max+1)
	for distance := 0; distance <= max; distance++ {
		current := make(map[int]int, distance*2+1)
		for k := -distance; k <= distance; k += 2 {
			var x int
			if k == -distance || (k != distance && v[k-1] < v[k+1]) {
				x = v[k+1]
			} else {
				x = v[k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			current[k] = x
			if x >= n && y >= m {
				trace = append(trace, current)
				return backtrackDiff(a, b, trace)
			}
		}
		trace = append(trace, current)
		v = current
	}
	return nil
}

func backtrackDiff(a, b []string, trace []map[int]int) []diffOp {
	x, y := len(a), len(b)
	backward := make([]diffOp, 0, x+y)
	for distance := len(trace) - 1; distance > 0; distance-- {
		k := x - y
		previous := trace[distance-1]
		var previousK int
		if k == -distance || (k != distance && previous[k-1] < previous[k+1]) {
			previousK = k + 1
		} else {
			previousK = k - 1
		}
		previousX := previous[previousK]
		previousY := previousX - previousK
		for x > previousX && y > previousY {
			x--
			y--
			backward = append(backward, diffOp{kind: ' ', line: a[x]})
		}
		if x == previousX {
			y--
			backward = append(backward, diffOp{kind: '+', line: b[y]})
		} else {
			x--
			backward = append(backward, diffOp{kind: '-', line: a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		backward = append(backward, diffOp{kind: ' ', line: a[x]})
	}
	for x > 0 {
		x--
		backward = append(backward, diffOp{kind: '-', line: a[x]})
	}
	for y > 0 {
		y--
		backward = append(backward, diffOp{kind: '+', line: b[y]})
	}
	for left, right := 0, len(backward)-1; left < right; left, right = left+1, right-1 {
		backward[left], backward[right] = backward[right], backward[left]
	}
	return backward
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	// Preserve whether final newline exists by using Split and dropping trailing empty
	// only when the string ends with \n — standard lines without keeping separators.
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	parts := strings.Split(normalized, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func normalizeDiffNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func hasFinalNewline(s string) bool {
	return strings.HasSuffix(s, "\n")
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
