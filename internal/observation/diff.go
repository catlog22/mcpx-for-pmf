package observation

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DiffMode controls how file.changed events are rendered in text mode.
type DiffMode uint8

const (
	// DiffModeFull keeps the complete inline diff. It is the default for
	// RenderText, which is also used by library callers.
	DiffModeFull DiffMode = iota
	DiffModePreview
	DiffModeSummary
)

func (m DiffMode) String() string {
	switch m {
	case DiffModePreview:
		return "preview"
	case DiffModeSummary:
		return "summary"
	default:
		return "full"
	}
}

// ParseDiffMode validates the user-facing workspace observer value.
func ParseDiffMode(value string) (DiffMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "full":
		return DiffModeFull, nil
	case "preview":
		return DiffModePreview, nil
	case "summary":
		return DiffModeSummary, nil
	default:
		return DiffModeFull, fmt.Errorf("diff mode must be summary, preview, or full")
	}
}

const (
	diffLineContext    = "context"
	diffLineAdded      = "added"
	diffLineRemoved    = "removed"
	diffLineFileHeader = "file_header"
	diffLineHunkHeader = "hunk_header"
	diffLineMetadata   = "metadata"
	diffLineNoNewline  = "no_newline"
)

type diffLine struct {
	kind    string
	text    string
	oldLine int
	newLine int
	hasOld  bool
	hasNew  bool
}

type diffDocument struct {
	lines   []diffLine
	added   int
	removed int
	hunks   int
}

var diffHunkPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

func parseDiffDocument(value string) diffDocument {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return diffDocument{}
	}

	document := diffDocument{lines: make([]diffLine, 0, strings.Count(value, "\n")+1)}
	oldLine, newLine := 0, 0
	inHunk := false
	for _, line := range strings.Split(value, "\n") {
		if matches := diffHunkPattern.FindStringSubmatch(line); matches != nil {
			oldLine = parseDiffNumber(matches[1], 0)
			newLine = parseDiffNumber(matches[3], 0)
			inHunk = true
			document.hunks++
			document.lines = append(document.lines, diffLine{kind: diffLineHunkHeader, text: line})
			continue
		}
		if strings.HasPrefix(line, "@@") {
			oldLine, newLine = 1, 1
			inHunk = true
			document.hunks++
			document.lines = append(document.lines, diffLine{kind: diffLineHunkHeader, text: line})
			continue
		}
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "diff --git ") {
			document.lines = append(document.lines, diffLine{kind: diffLineFileHeader, text: line})
			continue
		}
		if strings.HasPrefix(line, `\ No newline at end of file`) {
			document.lines = append(document.lines, diffLine{kind: diffLineNoNewline, text: line})
			continue
		}
		if !inHunk {
			document.lines = append(document.lines, diffLine{kind: diffLineMetadata, text: line})
			continue
		}

		parsed := diffLine{kind: diffLineContext, text: line, oldLine: oldLine, newLine: newLine, hasOld: oldLine > 0, hasNew: newLine > 0}
		switch {
		case strings.HasPrefix(line, "+"):
			parsed.kind = diffLineAdded
			parsed.hasOld = false
			document.added++
			newLine++
		case strings.HasPrefix(line, "-"):
			parsed.kind = diffLineRemoved
			parsed.hasNew = false
			document.removed++
			oldLine++
		default:
			oldLine++
			newLine++
		}
		document.lines = append(document.lines, parsed)
	}
	return document
}

func parseDiffNumber(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (d diffDocument) stats() string {
	if d.added == 0 && d.removed == 0 {
		return ""
	}
	return fmt.Sprintf("+%d -%d", d.added, d.removed)
}

type diffDocumentCache struct {
	documents map[string]diffDocument
	order     []string
}

func newDiffDocumentCache() *diffDocumentCache {
	return &diffDocumentCache{documents: make(map[string]diffDocument)}
}

func (c *diffDocumentCache) get(value string) diffDocument {
	if c == nil || value == "" {
		return parseDiffDocument(value)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
	if document, ok := c.documents[key]; ok {
		return document
	}
	document := parseDiffDocument(value)
	if len(c.documents) >= 64 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.documents, oldest)
	}
	c.documents[key] = document
	c.order = append(c.order, key)
	return document
}
