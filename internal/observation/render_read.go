package observation

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func contextQueryCommand(input map[string]any) string {
	action, _ := input["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "list" {
		parts := []string{"find"}
		paths := inputPaths(input)
		if len(paths) == 0 {
			parts = append(parts, ".")
		} else {
			parts = append(parts, paths...)
		}
		parts = append(parts, "-type", "f")
		if include, _ := input["include_glob"].(string); strings.TrimSpace(include) != "" {
			parts = append(parts, "-path", commandPatternArg(include))
		}
		return strings.Join(parts, " ")
	}

	parts := []string{"rg"}
	if include, _ := input["include_glob"].(string); strings.TrimSpace(include) != "" {
		parts = append(parts, "--glob", commandPatternArg(include))
	}
	if exclude, _ := input["exclude_glob"].(string); strings.TrimSpace(exclude) != "" {
		parts = append(parts, "--glob", commandPatternArg("!"+exclude))
	}
	if caseSensitive, exists := input["case_sensitive"].(bool); exists && !caseSensitive {
		parts = append(parts, "--ignore-case")
	}
	query, _ := input["query"].(string)
	if strings.TrimSpace(query) == "" {
		query = "<query>"
	}
	parts = append(parts, commandArg(query))
	parts = append(parts, inputPaths(input)...)
	return strings.Join(parts, " ")
}

func inputPaths(input map[string]any) []string {
	paths := make([]string, 0, 3)
	if raw, ok := input["paths"].([]any); ok {
		for _, value := range raw {
			if path, ok := value.(string); ok && strings.TrimSpace(path) != "" {
				paths = append(paths, commandArg(path))
				if len(paths) == 3 {
					break
				}
			}
		}
	}
	if len(paths) == 0 {
		if path, ok := input["path"].(string); ok && strings.TrimSpace(path) != "" {
			paths = append(paths, commandArg(path))
		}
	}
	return paths
}

func commandArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t\n\r\"'") {
		return strconv.Quote(value)
	}
	return value
}

func commandPatternArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	return strconv.Quote(value)
}

func inputMap(raw []byte) map[string]any {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return nil
	}
	return input
}

func readActionLabel(input map[string]any) string {
	if input == nil {
		return "read request"
	}
	view := strings.ToLower(strings.TrimSpace(stringValue(input["view"])))
	switch view {
	case "", "file":
		if input["path"] != nil || input["items"] != nil || view == "file" {
			return fileReadLabel(input)
		}
	case "list":
		scope := stringValue(input["path"])
		if scope == "" {
			scope = "."
		}
		return scope + " (list)"
	case "search", "context":
		query := stringValue(input["query"])
		if query == "" {
			query = "<query>"
		}
		scopes := inputPaths(input)
		if len(scopes) == 0 {
			scopes = []string{"."}
		}
		return view + " " + strconv.Quote(query) + " in " + strings.Join(scopes, ", ")
	case "environment":
		sections := stringValues(input["sections"], 6)
		if len(sections) == 0 {
			return "environment"
		}
		return "environment (" + strings.Join(sections, ", ") + ")"
	}
	if path := stringValue(input["path"]); path != "" {
		return path
	}
	if scopes := inputPaths(input); len(scopes) > 0 {
		return strings.Join(scopes, ", ")
	}
	if query := stringValue(input["query"]); query != "" {
		if view == "" {
			view = "query"
		}
		return view + " " + strconv.Quote(query)
	}
	if view != "" {
		return view
	}
	return "read request"
}

func fileReadLabel(input map[string]any) string {
	if input == nil {
		return "files"
	}
	if path, ok := input["path"].(string); ok && strings.TrimSpace(path) != "" {
		return strings.TrimSpace(path) + readScopeLabel(input)
	}
	items, _ := input["items"].([]any)
	labels := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		path, _ := item["path"].(string)
		if strings.TrimSpace(path) != "" {
			labels = append(labels, strings.TrimSpace(path)+readScopeLabel(item))
		}
	}
	if len(labels) == 1 {
		return labels[0]
	}
	if len(labels) > 1 {
		return fmt.Sprintf("%d files", len(labels))
	}
	return "files"
}

func fileReadDetailLines(tool string, raw []byte) []string {
	if !isFileReadInput(tool, raw) {
		return nil
	}
	input := inputMap(raw)
	items, _ := input["items"].([]any)
	if len(items) <= 1 {
		return nil
	}
	const maxReadItems = 20
	lines := make([]string, 0, minInt(len(items), maxReadItems)+1)
	for _, rawItem := range items[:minInt(len(items), maxReadItems)] {
		item, _ := rawItem.(map[string]any)
		path, _ := item["path"].(string)
		if path = strings.TrimSpace(path); path != "" {
			lines = append(lines, path+readScopeLabel(item))
		}
	}
	if len(items) > maxReadItems {
		lines = append(lines, fmt.Sprintf("... and %d more files", len(items)-maxReadItems))
	}
	return lines
}

func isFileReadInput(tool string, raw []byte) bool {
	input := inputMap(raw)
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "file_read":
		return true
	case "read", "source_read":
		view := strings.ToLower(strings.TrimSpace(stringValue(input["view"])))
		return view == "file" || (view == "" && (input["path"] != nil || input["items"] != nil))
	default:
		return false
	}
}

func readScopeLabel(input map[string]any) string {
	if input == nil {
		return ""
	}
	offset, hasOffset := integerValue(input["offset"])
	limit, hasLimit := integerValue(input["limit"])
	if hasOffset && offset < 0 {
		hasOffset = false
	}
	if hasLimit && limit <= 0 {
		hasLimit = false
	}
	if hasOffset || hasLimit {
		start := 1
		if hasOffset {
			start = offset + 1
		}
		if hasLimit {
			return fmt.Sprintf(" (lines %d-%d)", start, start+limit-1)
		}
		return fmt.Sprintf(" (from line %d)", start)
	}
	mode := strings.ToLower(strings.TrimSpace(stringValue(input["mode"])))
	if mode == "window" {
		return " (window)"
	}
	return " (full)"
}

func integerValue(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		return int(number), true
	case int:
		return number, true
	case int64:
		return int(number), true
	default:
		return 0, false
	}
}

func fileReadResultLabel(result map[string]any) string {
	structured, _ := result["structured_content"].(map[string]any)
	if structured == nil {
		return ""
	}
	if path, ok := structured["path"].(string); ok && strings.TrimSpace(path) != "" {
		return path
	}
	items, _ := structured["items"].([]any)
	paths := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		path, _ := item["path"].(string)
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 1 {
		return paths[0]
	}
	if len(paths) > 1 {
		return fmt.Sprintf("%d files (%s)", len(paths), strings.Join(paths[:minInt(len(paths), 3)], ", "))
	}
	return ""
}
