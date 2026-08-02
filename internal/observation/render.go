package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	ansiReset = "\033[0m"
	ansiGreen = "\033[32m"
	ansiBlue  = "\033[36m"
)

// RenderText writes a terminal-style timeline item. Tool-start events are
// intentionally silent: the corresponding completion event renders one
// past-tense action with a compact result, so replayed history does not show
// TOOL STARTED/COMPLETED protocol labels or duplicate actions.
func RenderText(w io.Writer, event Event, color bool) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	switch event.Type {
	case TypeToolStarted:
		return nil
	case TypeToolCompleted:
		return renderToolCompleted(w, event, color)
	case TypeCommandOutput:
		return renderCommandOutput(w, event, color)
	case TypeFileChanged:
		return renderFileChanged(w, event, color)
	case TypeSessionLifecycle:
		return renderSummaryEvent(w, lifecycleVerb(event.Summary), event.Summary, event.Output, color)
	case TypeObserverNotice:
		return renderSummaryEvent(w, "Observed", event.Summary, event.Output, color)
	default:
		return renderSummaryEvent(w, "Observed", event.Type, event.Output, color)
	}
}

// RenderJSON writes one complete JSON event per line for scripts and log
// ingestion. It intentionally does not wrap the event in a protocol frame.
func RenderJSON(w io.Writer, event Event) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	return json.NewEncoder(w).Encode(event)
}

func renderToolCompleted(w io.Writer, event Event, color bool) error {
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	verb, label := toolAction(event.Tool, event.Input)
	if event.Tool == "file_read" && label == "files" {
		if result, ok := payload["result"].(map[string]any); ok {
			if outputLabel := fileReadResultLabel(result); outputLabel != "" {
				label = outputLabel
			}
		}
	}
	if err := writeAction(w, verb, label, ansiBlue, color); err != nil {
		return err
	}

	details := make([]string, 0, 2)
	status, _ := payload["status"].(string)
	if status == "error" {
		if message := errorSummary(payload); message != "" {
			details = append(details, "failed: "+compactLine(message))
		}
	} else {
		if event.ProgressSummary != "" {
			details = append(details, compactLine(event.ProgressSummary))
		}
		if result, ok := payload["result"].(map[string]any); ok && len(details) < 2 {
			if output := humanToolOutput(event.Tool, result); output != "" {
				details = append(details, output)
			}
		}
	}
	if len(details) == 0 && status != "" && status != "ok" {
		details = append(details, strings.ReplaceAll(status, "_", " "))
	}
	if event.Truncated && len(details) < 2 {
		details = append(details, "output truncated; see the linked resource or task history")
	}
	if len(details) > 2 {
		details = details[:2]
	}
	return writeChildren(w, details, color)
}

func renderCommandOutput(w io.Writer, event Event, color bool) error {
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	text, _ := payload["text"].(string)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	stream := event.Stream
	if stream == "" {
		stream = "output"
	}
	if err := writeAction(w, "Read", stream, ansiBlue, color); err != nil {
		return err
	}
	lines := summaryLines(text, 2)
	if event.Truncated && len(lines) < 2 {
		lines = append(lines, "output truncated; see task logs")
	}
	return writeChildren(w, lines, color)
}

func renderFileChanged(w io.Writer, event Event, color bool) error {
	var payload struct {
		Files []struct {
			Path          string `json:"path"`
			NewPath       string `json:"new_path"`
			Operation     string `json:"operation"`
			Diff          string `json:"diff"`
			DiffTruncated bool   `json:"diff_truncated"`
		} `json:"files"`
		Diff struct {
			ResourceURI string `json:"resource_uri"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(event.Output, &payload); err != nil || len(payload.Files) == 0 {
		label := compactLine(event.Summary)
		if label == "" {
			label = "files"
		}
		if err := writeAction(w, "Edited", label, ansiGreen, color); err != nil {
			return err
		}
		return writeChildren(w, []string{"file details unavailable"}, color)
	}
	label := fmt.Sprintf("%d files", len(payload.Files))
	if len(payload.Files) == 1 {
		label = payload.Files[0].Path
	}
	if err := writeAction(w, "Edited", label, ansiGreen, color); err != nil {
		return err
	}
	for index, file := range payload.Files {
		if index >= maxChangedFiles {
			break
		}
		path := file.Path
		if file.NewPath != "" && file.NewPath != file.Path {
			path += " -> " + file.NewPath
		}
		if path == "" {
			path = "unknown file"
		}
		stats := diffStats(file.Diff)
		line := path
		if file.Operation != "" {
			line += " (" + file.Operation + ")"
		}
		if stats != "" {
			line += " " + stats
		}
		if err := writeChild(w, line, color); err != nil {
			return err
		}
		if file.Diff != "" {
			preview, truncated := diffPreview(file.Diff, maxFileDiffLines)
			for _, line := range preview {
				if err := writeCodeChild(w, line); err != nil {
					return err
				}
			}
			if truncated || file.DiffTruncated {
				if err := writeChild(w, "...", color); err != nil {
					return err
				}
			}
		}
		if file.DiffTruncated {
			if err := writeChild(w, "full diff is available from the linked resource", color); err != nil {
				return err
			}
		}
	}
	if len(payload.Files) > maxChangedFiles {
		if err := writeChild(w, fmt.Sprintf("... and %d more files", len(payload.Files)-maxChangedFiles), color); err != nil {
			return err
		}
	}
	if event.ResourceURI != "" {
		if err := writeChild(w, "full diff: "+event.ResourceURI, color); err != nil {
			return err
		}
	} else if payload.Diff.ResourceURI != "" {
		if err := writeChild(w, "full diff: "+payload.Diff.ResourceURI, color); err != nil {
			return err
		}
	}
	return nil
}

const (
	maxToolSummaryRunes = 240
	maxFileDiffLines    = 8
	maxChangedFiles     = 6
)

func writeAction(w io.Writer, verb, label, colorCode string, color bool) error {
	if label == "" {
		label = "operation"
	}
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint("•", colorCode, color), paint(verb, colorCode, color), compactLine(label))
	return err
}

func writeChildren(w io.Writer, values []string, color bool) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := writeChild(w, value, color); err != nil {
			return err
		}
	}
	return nil
}

func writeChild(w io.Writer, value string, color bool) error {
	_, err := fmt.Fprintf(w, "  %s %s\n", paint("↳", ansiBlue, color), compactLine(value))
	return err
}

func writeCodeChild(w io.Writer, value string) error {
	_, err := fmt.Fprintf(w, "    %s\n", compactCodeLine(value))
	return err
}

func renderSummaryEvent(w io.Writer, verb, summary string, raw []byte, color bool) error {
	if strings.TrimSpace(summary) == "" {
		summary = "event"
	}
	if err := writeAction(w, verb, summary, ansiBlue, color); err != nil {
		return err
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil {
		if details := compactMap(value); details != "" {
			return writeChildren(w, []string{details}, color)
		}
	}
	return nil
}

func lifecycleVerb(summary string) string {
	value := strings.ToLower(summary)
	switch {
	case strings.Contains(value, "created"), strings.Contains(value, "opened"):
		return "Opened"
	case strings.Contains(value, "closed"):
		return "Closed"
	case strings.Contains(value, "updated"):
		return "Updated"
	default:
		return "Observed"
	}
}

func toolAction(tool string, raw []byte) (string, string) {
	input := inputMap(raw)
	action, _ := input["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	verb := actionVerb(tool, action)
	label := ""
	switch tool {
	case "command_execute":
		label, _ = input["command"].(string)
		if strings.TrimSpace(label) == "" {
			label, _ = input["task"].(string)
		}
		label = compactCommand(label)
	case "file_read":
		label = fileReadLabel(input)
	case "context_query":
		label = contextQueryCommand(input)
	case "change_execute":
		label = changeLabel(input)
	case "progress_report":
		label, _ = input["summary"].(string)
	case "workspace_list":
		label = "workspaces"
	case "session_open":
		label, _ = input["workspace"].(string)
		if strings.TrimSpace(label) == "" {
			label, _ = input["remote_session_id"].(string)
		}
	default:
		for _, key := range []string{"workspace", "path", "changeset_id", "task_id", "artifact_id", "remote_session_id"} {
			if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
				label = value
				break
			}
		}
		if label == "" {
			label = action
		}
	}
	if strings.TrimSpace(label) == "" {
		label = strings.ReplaceAll(tool, "_", " ")
	}
	return verb, label
}

func actionVerb(tool, action string) string {
	switch tool {
	case "command_execute":
		return "Ran"
	case "context_query":
		return "Searched"
	case "file_read":
		return "Read"
	case "change_execute":
		return "Edited"
	case "session_open":
		return "Opened"
	case "workspace_list":
		return "Listed"
	case "progress_report":
		return "Reported"
	case "screenshot_capture":
		return "Captured"
	}
	switch action {
	case "create", "created", "register", "prepare":
		return "Created"
	case "edit", "update", "apply", "write":
		return "Edited"
	case "search", "query", "list", "get", "read", "diff", "history", "status", "inspect":
		return "Read"
	case "execute", "run", "call":
		return "Ran"
	default:
		return "Observed"
	}
}

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

func fileReadLabel(input map[string]any) string {
	if input == nil {
		return "files"
	}
	if path, ok := input["path"].(string); ok && strings.TrimSpace(path) != "" {
		return path
	}
	items, _ := input["items"].([]any)
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
	return "files"
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

func changeLabel(input map[string]any) string {
	if summary, ok := input["summary"].(string); ok && strings.TrimSpace(summary) != "" {
		return summary
	}
	if id, ok := input["changeset_id"].(string); ok && strings.TrimSpace(id) != "" {
		return "changeset " + id
	}
	operations, _ := input["operations"].([]any)
	if len(operations) == 1 {
		operation, _ := operations[0].(map[string]any)
		if path, ok := operation["path"].(string); ok && path != "" {
			return path
		}
	}
	if len(operations) > 1 {
		return fmt.Sprintf("%d files", len(operations))
	}
	return "files"
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func errorSummary(payload map[string]any) string {
	if message, ok := payload["error"].(string); ok {
		return message
	}
	if details, ok := payload["error"].(map[string]any); ok {
		if message, ok := details["message"].(string); ok && message != "" {
			return message
		}
		return compactMap(details)
	}
	return "operation failed"
}

func diffStats(diff string) string {
	added, removed := 0, 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			added++
		} else if strings.HasPrefix(line, "-") {
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return ""
	}
	return fmt.Sprintf("+%d -%d", added, removed)
}

func diffPreview(diff string, maxLines int) ([]string, bool) {
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines, false
	}
	return lines[:maxLines], true
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

func compactCommand(command string) string {
	command = strings.TrimSpace(strings.SplitN(command, "\n", 2)[0])
	if command == "" {
		return "command_execute"
	}
	runes := []rune(command)
	if len(runes) > maxToolSummaryRunes {
		return string(runes[:maxToolSummaryRunes-3]) + "..."
	}
	return command
}
func humanToolOutput(tool string, result map[string]any) string {
	if tool == "context_query" {
		if summary := contextQueryOutputSummary(result); summary != "" {
			return summary
		}
	}
	if data := remoteEnvelopeData(result); data != nil {
		if summary := remoteDataSummary(tool, data); summary != "" {
			return summary
		}
	}
	textBlocks := textContentBlocks(result)
	if len(textBlocks) > 0 {
		summaries := make([]string, 0, len(textBlocks))
		for _, text := range textBlocks {
			if summary := toolOutputSummary(text); summary != "" {
				summaries = append(summaries, summary)
			}
		}
		return strings.Join(summaries, " · ")
	}
	structured, _ := result["structured_content"].(map[string]any)
	if structured != nil {
		return compactMap(structured)
	}
	return ""
}

func remoteEnvelopeData(result map[string]any) map[string]any {
	rawContent, _ := result["content"].([]any)
	for _, raw := range rawContent {
		item, _ := raw.(map[string]any)
		if item["type"] != "text" {
			continue
		}
		text, _ := item["text"].(string)
		var envelope map[string]any
		if json.Unmarshal([]byte(text), &envelope) != nil {
			continue
		}
		data, _ := envelope["data"].(map[string]any)
		if data != nil {
			return data
		}
	}
	return nil
}

func remoteDataSummary(tool string, data map[string]any) string {
	switch tool {
	case "workspace_list":
		items, _ := data["workspaces"].([]any)
		if len(items) == 0 {
			return "No registered workspaces."
		}
		paths := make([]string, 0, len(items))
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			name, _ := item["name"].(string)
			path, _ := item["path"].(string)
			if name == "" && path == "" {
				continue
			}
			if path != "" {
				paths = append(paths, name+" ("+path+")")
			} else {
				paths = append(paths, name)
			}
		}
		return fmt.Sprintf("Available workspaces: %s", compactPathList(paths))
	case "session_open":
		remote, _ := data["remote_session"].(map[string]any)
		workspace, _ := data["workspace"].(map[string]any)
		id, _ := remote["id"].(string)
		name, _ := workspace["name"].(string)
		if id == "" {
			return ""
		}
		if name == "" {
			name, _ = remote["workspace_name"].(string)
		}
		return fmt.Sprintf("Session %s opened for workspace %s.", id, name)
	}
	return ""
}

func contextQueryOutputSummary(result map[string]any) string {
	structured, _ := result["structured_content"].(map[string]any)
	if structured == nil {
		return ""
	}
	summary := ""
	if blocks := textContentBlocks(result); len(blocks) > 0 {
		summary = blocks[0]
	}
	paths := make([]string, 0)
	if matches, ok := structured["matches"].([]any); ok {
		for _, raw := range matches {
			item, _ := raw.(map[string]any)
			path, _ := item["path"].(string)
			if strings.TrimSpace(path) == "" {
				continue
			}
			if line := formatNumber(item["line"]); line != "" && line != "0" {
				path += ":" + line
			}
			paths = append(paths, path)
		}
	}
	if files, ok := structured["files"].([]any); ok && len(paths) == 0 {
		for _, raw := range files {
			item, _ := raw.(map[string]any)
			path, _ := item["path"].(string)
			if strings.TrimSpace(path) != "" {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		return summary
	}
	if summary == "" {
		summary = fmt.Sprintf("Found %d result(s)", len(paths))
	}
	summary = strings.TrimSuffix(strings.TrimSpace(summary), ".")
	return compactLine(summary + ": " + compactPathList(paths))
}

func compactPathList(paths []string) string {
	if len(paths) <= 4 {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:4], ", ") + fmt.Sprintf(", ... +%d more", len(paths)-4)
}

func toolOutputSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, marker := range []string{"\n### ", "\n```", "\n> "} {
		if index := strings.Index(text, marker); index >= 0 {
			text = text[:index]
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "# ")
		runes := []rune(line)
		if len(runes) > maxToolSummaryRunes {
			return string(runes[:maxToolSummaryRunes-3]) + "..."
		}
		return line
	}
	return ""
}

func summaryLines(text string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	for _, raw := range strings.Split(strings.TrimSpace(text), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if parsed := humanText(line); parsed != line {
			line = parsed
		}
		lines = append(lines, compactLine(line))
		if len(lines) == maxLines {
			break
		}
	}
	return lines
}

func textContentBlocks(result map[string]any) []string {
	var textBlocks []string
	rawContent, ok := result["content"].([]any)
	if !ok {
		return nil
	}
	for _, raw := range rawContent {
		item, _ := raw.(map[string]any)
		if item["type"] != "text" {
			continue
		}
		text, _ := item["text"].(string)
		if text = humanText(text); text != "" {
			textBlocks = append(textBlocks, text)
		}
	}
	return textBlocks
}

func humanText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return text
	}
	if object, ok := value.(map[string]any); ok {
		return compactMap(object)
	}
	return fmt.Sprint(value)
}

func compactMap(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if formatted := compactValue(value[key]); formatted != "" {
			parts = append(parts, key+"="+formatted)
		}
	}
	return strings.Join(parts, " ")
}

func compactValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		line := strings.TrimSpace(strings.SplitN(typed, "\n", 2)[0])
		if line == "" {
			return ""
		}
		return strconv.Quote(line)
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return formatNumber(typed)
	case []any:
		return fmt.Sprintf("%d items", len(typed))
	case []map[string]any:
		return fmt.Sprintf("%d items", len(typed))
	case map[string]any:
		return "object"
	default:
		return fmt.Sprint(typed)
	}
}

func formatNumber(value any) string {
	switch number := value.(type) {
	case float64:
		return fmt.Sprintf("%.0f", number)
	case int:
		return fmt.Sprintf("%d", number)
	case int64:
		return fmt.Sprintf("%d", number)
	default:
		return ""
	}
}

func paint(value, code string, enabled bool) string {
	if !enabled {
		return value
	}
	return code + value + ansiReset
}
