package arc

import (
	"fmt"
	"strings"
)

// Presentation describes host-visible renderers without changing result data.
type Presentation struct {
	Default   string         `json:"default"`
	Available []string       `json:"available"`
	Fallback  []string       `json:"fallback"`
	Options   map[string]any `json:"options,omitempty"`
}

type PresentationPreference struct {
	Preferred  string   `json:"preferred,omitempty"`
	Fallback   []string `json:"fallback,omitempty"`
	ShowSource bool     `json:"show_source,omitempty"`
	Density    string   `json:"density,omitempty"`
}

type HostCapabilities struct {
	Renderers []string            `json:"renderers"`
	Formats   map[string][]string `json:"formats,omitempty"`
}

// DefaultPresentation returns the renderer contract for a stable ARC result type.
func DefaultPresentation(resultType string) *Presentation {
	var presentation Presentation
	switch resultType {
	case "markdown":
		presentation = Presentation{Default: "markdown", Available: []string{"markdown", "text"}, Fallback: []string{"text"}}
	case "table", "search_result":
		presentation = Presentation{Default: "table", Available: []string{"table", "source", "markdown", "text"}, Fallback: []string{"source", "markdown", "text"}}
	case "code_change":
		presentation = Presentation{Default: "diff", Available: []string{"diff", "source", "markdown", "text"}, Fallback: []string{"source", "markdown", "text"}}
	case "file_tree":
		presentation = Presentation{Default: "tree", Available: []string{"tree", "markdown", "text"}, Fallback: []string{"markdown", "text"}}
	case "log":
		presentation = Presentation{Default: "log", Available: []string{"log", "source", "text"}, Fallback: []string{"text"}}
	case "diagram", "diagram_collection":
		presentation = Presentation{Default: "diagram", Available: []string{"diagram", "source", "markdown", "text"}, Fallback: []string{"markdown", "text"}}
	case "plan":
		presentation = Presentation{Default: "task_list", Available: []string{"task_list", "table", "markdown", "text"}, Fallback: []string{"markdown", "text"}}
	case "delivery":
		presentation = Presentation{Default: "markdown", Available: []string{"markdown", "table", "text"}, Fallback: []string{"table", "text"}}
	default:
		presentation = Presentation{Default: "text", Available: []string{"text"}, Fallback: []string{"text"}}
	}
	return &presentation
}

// NormalizePresentation removes invalid and duplicate renderer names and always
// leaves a usable text fallback. It is intentionally pure so hosts can apply it
// without changing the business result.
func NormalizePresentation(input Presentation) Presentation {
	available := knownRenderers(input.Available)
	if len(available) == 0 {
		available = []string{"text"}
	}
	if !contains(available, "text") {
		available = append(available, "text")
	}
	fallback := filterAvailable(knownRenderers(input.Fallback), available)
	fallback = withTextFallback(fallback)
	if !contains(available, input.Default) {
		input.Default = available[0]
	}
	input.Available = available
	input.Fallback = fallback
	return input
}

func withTextFallback(values []string) []string {
	result := make([]string, 0, len(values)+1)
	for _, value := range values {
		if value != "text" {
			result = append(result, value)
		}
	}
	return append(result, "text")
}

// SelectRenderer applies model preference only when it is advertised by the
// result and supported by the host, then falls back to the result contract.
func SelectRenderer(presentation Presentation, preference PresentationPreference, host HostCapabilities) string {
	presentation = NormalizePresentation(presentation)
	if preferred := preference.Preferred; contains(presentation.Available, preferred) && hostSupports(host, preferred) {
		return preferred
	}
	for _, preferred := range preference.Fallback {
		if contains(presentation.Available, preferred) && hostSupports(host, preferred) {
			return preferred
		}
	}
	if hostSupports(host, presentation.Default) {
		return presentation.Default
	}
	for _, fallback := range presentation.Fallback {
		if hostSupports(host, fallback) {
			return fallback
		}
	}
	return "text"
}

func hostSupports(host HostCapabilities, renderer string) bool {
	if renderer == "" {
		return false
	}
	if len(host.Renderers) == 0 {
		return true
	}
	return contains(host.Renderers, renderer)
}

// renderDiffMaxLines caps the inline diff shown in the host-visible text.
// Larger diffs point to the Changeset Resource (see change_execute results).
const renderDiffMaxLines = 200

// RenderContent builds the host-visible text for a result according to the
// selected renderer. It returns the rendered content and whether a dedicated
// view was produced; callers fall back to the plain summary when it returns
// false. All hosts receive the same rendered content; the machine-readable
// ARC envelope lives in structuredContent.
func RenderContent(resultType, renderer, summary string, data any) (string, bool) {
	if resultType != "code_change" || renderer != "diff" {
		return summary, false
	}
	asMap, _ := data.(map[string]any)
	if asMap == nil {
		return summary, false
	}
	return renderCodeChange(asMap), true
}

func renderCodeChange(data map[string]any) string {
	var b strings.Builder
	changesetID, _ := data["changeset_id"].(string)
	if changesetID != "" {
		fmt.Fprintf(&b, "### Changeset %s", changesetID)
	}
	if status, _ := data["status"].(string); status == "need_confirmation" {
		b.WriteString("\n\n> ⚠️ 此变更需要确认后才能应用。")
	}
	diffMeta, _ := data["diff"].(map[string]any)
	diffText, _ := diffMeta["unified_diff"].(string)
	if diffText == "" {
		diffText, _ = diffMeta["unified_diff_preview"].(string)
	}
	totalAdded, totalRemoved := countDiffLines(diffText)
	if totalAdded > 0 || totalRemoved > 0 {
		fmt.Fprintf(&b, " · +%d −%d", totalAdded, totalRemoved)
	}
	fileStats := diffStatsByPath(diffText)
	files := changeFiles(data["files"])
	if len(files) > 0 {
		b.WriteString("\n\n| 文件 | 操作 | 变更 |\n|---|---|---|\n")
		for _, file := range files {
			path, _ := file["path"].(string)
			op, _ := file["operation"].(string)
			if path == "" {
				continue
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", path, op, formatDiffStats(fileStats[path]))
		}
	}
	fileDiffRendered, fileDiffTruncated := renderFileDiffs(&b, files)
	if !fileDiffRendered && diffText != "" {
		display, truncated := trimDiffForRender(diffText)
		b.WriteString("\n```diff\n")
		b.WriteString(display)
		b.WriteString("\n```\n")
		if truncated {
			resourceURI, _ := diffMeta["resource_uri"].(string)
			if resourceURI != "" {
				b.WriteString("\n> 剩余部分见 Changeset Resource。\n")
			}
		}
	} else if fileDiffRendered {
		fileDiffTruncated = fileDiffTruncated || diffMeta["mode"] == "resource"
		if fileDiffTruncated {
			resourceURI, _ := diffMeta["resource_uri"].(string)
			if resourceURI != "" {
				b.WriteString("\n\n> 完整变更见 Changeset Resource。")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func changeFiles(value any) []map[string]any {
	switch files := value.(type) {
	case []map[string]any:
		return files
	case []any:
		result := make([]map[string]any, 0, len(files))
		for _, raw := range files {
			if file, ok := raw.(map[string]any); ok {
				result = append(result, file)
			}
		}
		return result
	default:
		return nil
	}
}

func renderFileDiffs(builder *strings.Builder, files []map[string]any) (rendered, truncated bool) {
	for _, file := range files {
		diff, _ := file["diff"].(string)
		if strings.TrimSpace(diff) == "" {
			continue
		}
		path, _ := file["path"].(string)
		if path == "" {
			continue
		}
		label := path
		if newPath, _ := file["new_path"].(string); newPath != "" && newPath != path {
			label += " → " + newPath
		}
		op, _ := file["operation"].(string)
		builder.WriteString("\n\n#### `")
		builder.WriteString(label)
		builder.WriteString("`")
		if op != "" {
			builder.WriteString(" · ")
			builder.WriteString(op)
		}
		builder.WriteString("\n\n```diff\n")
		builder.WriteString(diff)
		builder.WriteString("\n```")
		rendered = true
		if value, _ := file["diff_truncated"].(bool); value {
			truncated = true
		}
	}
	return rendered, truncated
}

type diffStats struct {
	added   int
	removed int
}

// diffStatsByPath scans a unified diff once so rendering a changeset with many
// files does not repeatedly split and rescan the full diff for every row.
func diffStatsByPath(diffText string) map[string]diffStats {
	stats := make(map[string]diffStats)
	path := ""
	oldPath := ""
	inHunk := false
	for _, line := range strings.Split(diffText, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			path = diffPathFromHeader(line)
			inHunk = false
			if path != "" {
				stats[path] = diffStats{}
			}
			continue
		}
		if !inHunk && strings.HasPrefix(line, "--- ") {
			oldPath = strings.TrimPrefix(line, "--- ")
			continue
		}
		if !inHunk && strings.HasPrefix(line, "+++ ") {
			path = diffPathFromUnifiedHeaders(oldPath, strings.TrimPrefix(line, "+++ "))
			if path != "" {
				stats[path] = diffStats{}
			}
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			continue
		}
		if !inHunk || path == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			current := stats[path]
			current.added++
			stats[path] = current
		case strings.HasPrefix(line, "-"):
			current := stats[path]
			current.removed++
			stats[path] = current
		}
	}
	return stats
}

func diffPathFromUnifiedHeaders(oldPath, newPath string) string {
	oldPath = strings.TrimPrefix(oldPath, "a/")
	newPath = strings.TrimPrefix(newPath, "b/")
	if newPath != "/dev/null" && newPath != "" {
		return newPath
	}
	return oldPath
}

func diffPathFromHeader(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	separator := strings.Index(rest, " b/")
	if separator <= 2 || !strings.HasPrefix(rest, "a/") {
		return ""
	}
	return rest[2:separator]
}

func formatDiffStats(stats diffStats) string {
	if stats.added == 0 && stats.removed == 0 {
		return "±0"
	}
	return fmt.Sprintf("+%d −%d", stats.added, stats.removed)
}

func countDiffLines(diffText string) (added, removed int) {
	for _, line := range strings.Split(diffText, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func trimDiffForRender(diffText string) (string, bool) {
	lines := strings.Split(diffText, "\n")
	if len(lines) <= renderDiffMaxLines {
		return diffText, false
	}
	return strings.Join(lines[:renderDiffMaxLines], "\n"), true
}

// DetectMermaid recognizes complete, renderer-only Mermaid content. Plain
// prose, a Mermaid keyword, a non-Mermaid fence, and an unterminated fence are
// deliberately left as Markdown/Text so parsing never changes business data.
func DetectMermaid(text string) (string, string, bool) {
	blocks := extractMermaidBlocks(strings.TrimSpace(text))
	if len(blocks) == 0 {
		return "", "", false
	}
	if len(blocks) == 1 {
		return "diagram", blocks[0], true
	}
	return "diagram_collection", strings.Join(blocks, "\n\n"), true
}

func extractMermaidBlocks(text string) []string {
	remaining := strings.TrimSpace(text)
	var blocks []string
	for remaining != "" {
		if !strings.HasPrefix(remaining, "```") {
			return nil
		}
		remaining = remaining[len("```"):]
		lineEnd := strings.IndexByte(remaining, '\n')
		if lineEnd < 0 || strings.TrimSpace(strings.TrimSuffix(remaining[:lineEnd], "\r")) != "mermaid" {
			return nil
		}
		remaining = remaining[lineEnd+1:]
		end := strings.Index(remaining, "```")
		if end < 0 {
			return nil
		}
		source := strings.TrimSpace(remaining[:end])
		if !isMermaidSource(source) {
			return nil
		}
		blocks = append(blocks, source)
		remaining = strings.TrimSpace(remaining[end+len("```"):])
	}
	return blocks
}

func isMermaidSource(source string) bool {
	lower := strings.ToLower(strings.TrimSpace(source))
	for _, prefix := range []string{
		"flowchart ", "graph ", "sequencediagram", "classdiagram", "statediagram",
		"erdiagram", "journey", "gantt", "pie ", "mindmap", "timeline", "gitgraph",
		"quadrantchart", "xychart", "block-beta", "architecture", "sankey-beta",
		"requirementdiagram", "c4context", "kanban", "packet-beta",
	} {
		if strings.HasPrefix(lower, prefix) && strings.TrimSpace(lower[len(prefix):]) != "" {
			return true
		}
	}
	return false
}

func knownRenderers(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range uniqueNonEmpty(values) {
		if isKnownRenderer(value) {
			result = append(result, value)
		}
	}
	return result
}

func isKnownRenderer(value string) bool {
	switch value {
	case "task_list", "table", "diagram", "diff", "tree", "log", "source", "markdown", "text":
		return true
	default:
		return false
	}
}

func uniqueNonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func filterAvailable(values, available []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if contains(available, value) && !contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
