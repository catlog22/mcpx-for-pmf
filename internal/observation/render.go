package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// RenderText writes a terminal-style timeline item. Tool-start events are
// intentionally silent: the corresponding completion event renders one
// past-tense action with a compact result, so replayed history does not show
// TOOL STARTED/COMPLETED protocol labels or duplicate actions.
func RenderText(w io.Writer, event Event, color bool) error {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode}, false)
}

func renderText(w io.Writer, event Event, color, suppressCommandOutput bool) error {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode}, suppressCommandOutput)
}

type renderOptions struct {
	colorMode     ColorMode
	terminalWidth int
}

func renderTextWithOptions(w io.Writer, event Event, options renderOptions, suppressCommandOutput bool) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	switch event.Type {
	case TypeToolStarted:
		return nil
	case TypeToolCompleted:
		return renderToolCompleted(w, event, options, suppressCommandOutput)
	case TypeCommandOutput:
		return renderCommandOutput(w, event, options)
	case TypeFileChanged:
		return renderFileChanged(w, event, options)
	case TypeSessionLifecycle:
		return renderSummaryEvent(w, event, lifecycleVerb(event.Summary), event.Summary, event.Output, options.colorMode != ColorModeNone)
	case TypeObserverNotice:
		return renderSummaryEvent(w, event, "Observed", event.Summary, event.Output, options.colorMode != ColorModeNone)
	default:
		return renderSummaryEvent(w, event, "Observed", event.Type, event.Output, options.colorMode != ColorModeNone)
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

func renderToolCompleted(w io.Writer, event Event, options renderOptions, suppressCommandOutput bool) error {
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
	status, _ := payload["status"].(string)
	failed, failureMessage := toolFailure(payload)
	if err := writeAction(w, verb, label, actionColor(event.Tool, failed), options.colorMode != ColorModeNone); err != nil {
		return err
	}

	details := make([]string, 0, 2)
	if failed {
		if failureMessage == "" {
			failureMessage = errorSummary(payload)
		}
		if message := failureMessage; message != "" {
			details = append(details, "failed: "+compactLine(message))
		}
	} else {
		if event.ProgressSummary != "" {
			details = append(details, compactLine(event.ProgressSummary))
		}
		if result, ok := payload["result"].(map[string]any); ok && len(details) < 2 {
			if output := humanToolOutput(event.Tool, result); output != "" {
				if suppressCommandOutput && event.Tool == "command_execute" {
					output = commandCompletionSummary(output)
				}
				details = append(details, output)
			}
		}
	}
	if len(details) == 0 && status != "" && status != "ok" {
		details = append(details, strings.ReplaceAll(status, "_", " "))
	}
	if len(details) > 2 {
		details = details[:2]
	}
	return writeChildren(w, details, options.colorMode != ColorModeNone)
}

func renderCommandOutput(w io.Writer, event Event, options renderOptions) error {
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
	if err := writeAction(w, "Read", stream, actionColor(event.Tool, false), options.colorMode != ColorModeNone); err != nil {
		return err
	}
	lines := summaryLines(text, 2)
	return writeChildren(w, lines, options.colorMode != ColorModeNone)
}

func renderFileChanged(w io.Writer, event Event, options renderOptions) error {
	var payload struct {
		DeleteSummary *struct {
			Display string `json:"display"`
		} `json:"delete_summary"`
		Files []struct {
			Path          string `json:"path"`
			NewPath       string `json:"new_path"`
			Operation     string `json:"operation"`
			Diff          string `json:"diff"`
			DiffTruncated bool   `json:"diff_truncated"`
		} `json:"files"`
	}
	if err := json.Unmarshal(event.Output, &payload); err != nil || len(payload.Files) == 0 {
		label := compactLine(event.Summary)
		if label == "" {
			label = "files"
		}
		if err := writeAction(w, "Edited", label, ansiGreen, options.colorMode != ColorModeNone); err != nil {
			return err
		}
		return writeChildren(w, []string{"file details unavailable"}, options.colorMode != ColorModeNone)
	}
	label := fmt.Sprintf("%d files", len(payload.Files))
	if len(payload.Files) == 1 {
		label = payload.Files[0].Path
	}
	if err := writeAction(w, "Edited", label, actionColor(event.toolOrType(), false), options.colorMode != ColorModeNone); err != nil {
		return err
	}
	if payload.DeleteSummary != nil && strings.TrimSpace(payload.DeleteSummary.Display) != "" {
		if err := writeChild(w, payload.DeleteSummary.Display, options.colorMode != ColorModeNone); err != nil {
			return err
		}
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
		if err := writeChild(w, line, options.colorMode != ColorModeNone); err != nil {
			return err
		}
		if file.Diff != "" {
			preview, truncated := diffPreview(file.Diff, maxFileDiffLines)
			for _, line := range preview {
				codeWidth := options.terminalWidth - 2 - 4
				if err := writeCodeChild(w, line, options, codeWidth); err != nil {
					return err
				}
			}
			if truncated || file.DiffTruncated {
				if err := writeChild(w, "...", options.colorMode != ColorModeNone); err != nil {
					return err
				}
			}
		}
	}
	if len(payload.Files) > maxChangedFiles {
		if err := writeChild(w, fmt.Sprintf("... and %d more files", len(payload.Files)-maxChangedFiles), options.colorMode != ColorModeNone); err != nil {
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

func writeCodeChild(w io.Writer, value string, options renderOptions, width int) error {
	value = compactCodeLine(value)
	if width > 0 {
		value = formatDiffLine(value, options.colorMode, width)
	} else {
		value = diffLineStyle(value, options.colorMode)
	}
	_, err := fmt.Fprintf(w, "    %s\n", value)
	return err
}

func renderSummaryEvent(w io.Writer, event Event, verb, summary string, raw []byte, color bool) error {
	if strings.TrimSpace(summary) == "" {
		summary = "event"
	}
	if err := writeAction(w, verb, summary, actionColor(event.toolOrType(), false), color); err != nil {
		return err
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil {
		if details := summaryEventOutput(event, value); details != "" {
			return writeChildren(w, []string{details}, color)
		}
	}
	return nil
}

func summaryEventOutput(event Event, value map[string]any) string {
	if event.Type != TypeObserverNotice && event.Type != TypeSessionLifecycle {
		return compactMap(value)
	}
	// Remote events already carry their useful information in the action title
	// (for example, "command.started: go test ./..."). Their source sequence
	// and nested metadata are transport details, not a useful terminal summary.
	metadata, _ := value["metadata"].(map[string]any)
	if len(metadata) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"changeset_id", "task_id", "status", "exit_code"} {
		if text := strings.TrimSpace(formatNumber(metadata[key])); text != "" {
			parts = append(parts, key+"="+text)
		}
	}
	return strings.Join(parts, " ")
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

	// Special case: skill/MCP calls via extension_manage (or skill_execute) should display
	// the skill/MCP name in terminal observation instead of "extension_manage".
	if (tool == "extension_manage" || tool == "skill_execute") && action == "call" {
		if kind, ok := input["kind"].(string); ok {
			kind = strings.ToLower(strings.TrimSpace(kind))
			if kind == "skill" {
				if name, ok := input["name"].(string); ok && strings.TrimSpace(name) != "" {
					label = name
				}
			} else if kind == "mcp" {
				if name, ok := input["server"].(string); ok && strings.TrimSpace(name) != "" {
					label = name
				}
			}
		}
	}

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
	case "runtime_inspect":
		label = runtimeInspectLabel(action)
	case "workspace_state":
		label = workspaceStateLabel(action)
	case "screenshot_capture":
		label = "screenshot"
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
	case "runtime_inspect":
		return "Read"
	case "workspace_state":
		if action == "snapshot" {
			return "Created"
		}
		return "Read"
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

func runtimeInspectLabel(action string) string {
	switch action {
	case "project":
		return "project summary"
	case "capabilities":
		return "runtime capabilities"
	case "instructions":
		return "agent instructions"
	default:
		return "runtime"
	}
}

func workspaceStateLabel(action string) string {
	switch action {
	case "changes":
		return "Git changes"
	case "snapshot":
		return "file snapshot"
	case "diff":
		return "file changes"
	case "watch":
		return "file watch"
	case "memory":
		return "project memory"
	default:
		return "workspace state"
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
		return compactLine(message)
	}
	if details, ok := payload["error"].(map[string]any); ok {
		return formatErrorDetails(details)
	}
	if result, ok := payload["result"].(map[string]any); ok {
		if message := nestedErrorSummary(result, true); message != "" {
			return message
		}
	}
	return "operation failed"
}

func toolFailure(payload map[string]any) (bool, string) {
	if status, _ := payload["status"].(string); status == "error" {
		return true, errorSummary(payload)
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return false, ""
	}
	status, _ := payload["status"].(string)
	isError, _ := payload["is_error"].(bool)
	if message := nestedErrorSummary(result, status == "error" || isError); message != "" {
		return true, message
	}
	return false, ""
}

func nestedErrorSummary(result map[string]any, allowPlainText bool) string {
	rawContent, _ := result["content"].([]any)
	for _, raw := range rawContent {
		item, _ := raw.(map[string]any)
		if item["type"] != "text" {
			continue
		}
		text, _ := item["text"].(string)
		var envelope map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(text)), &envelope) != nil {
			if allowPlainText {
				if message := compactLine(text); message != "" {
					return message
				}
			}
			continue
		}
		if summary := errorEnvelopeSummary(envelope); summary != "" {
			return summary
		}
		if status, _ := envelope["status"].(string); status == "error" {
			if message, _ := envelope["message"].(string); strings.TrimSpace(message) != "" {
				return compactLine(message)
			}
			return "operation failed"
		}
	}
	return ""
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

func commandCompletionSummary(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return compactLine(line)
		}
	}
	return ""
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
	if structured, _ := result["structured_content"].(map[string]any); structured != nil {
		if summary := structuredToolOutputSummary(tool, structured); summary != "" {
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

func structuredToolOutputSummary(tool string, data map[string]any) string {
	switch tool {
	case "extension_manage":
		return extensionManageOutputSummary(data)
	case "runtime_inspect":
		return runtimeInspectOutputSummary(data)
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
	case "plan_manage":
		return planManageOutputSummary(data)
	case "environment_inspect":
		return environmentInspectOutputSummary(data)
	case "runtime_inspect":
		return runtimeInspectOutputSummary(data)
	case "workspace_state":
		return workspaceStateOutputSummary(data)
	case "screenshot_capture":
		return screenshotCaptureOutputSummary(data)
	}
	return ""
}

func extensionManageOutputSummary(data map[string]any) string {
	parts := make([]string, 0, 2)
	if skills, exists := data["skills"]; exists {
		names := namedItems(skills, 6)
		if len(names) == 0 {
			parts = append(parts, "Skill：无匹配项")
		} else {
			parts = append(parts, fmt.Sprintf("Skill %d 项：%s", collectionLength(skills), strings.Join(names, "、")))
		}
	}
	if servers, exists := data["upstream_mcp"]; exists {
		names := namedItems(servers, 6)
		if len(names) == 0 {
			parts = append(parts, "MCP：无匹配项")
		} else {
			parts = append(parts, fmt.Sprintf("MCP %d 项：%s", collectionLength(servers), strings.Join(names, "、")))
		}
	}
	if skill, ok := data["skill"].(map[string]any); ok {
		if name, _ := skill["name"].(string); strings.TrimSpace(name) != "" {
			parts = append(parts, "Skill 已描述："+strings.TrimSpace(name))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；") + "。"
}

func namedItems(value any, limit int) []string {
	items, _ := value.([]any)
	if len(items) == 0 || limit <= 0 {
		return nil
	}
	names := make([]string, 0, minInt(len(items), limit))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		name, _ := item["name"].(string)
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
			if len(names) == limit {
				break
			}
		}
	}
	return names
}

func planManageOutputSummary(data map[string]any) string {
	planID, _ := data["plan_id"].(string)
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return ""
	}
	details := make([]string, 0, 3)
	if taskID, _ := data["task_id"].(string); strings.TrimSpace(taskID) != "" {
		details = append(details, "任务 "+strings.TrimSpace(taskID))
	}
	if status, _ := data["status"].(string); strings.TrimSpace(status) != "" {
		details = append(details, "状态 "+strings.TrimSpace(status))
	}
	if tasks, ok := data["tasks"].([]any); ok && len(tasks) > 0 {
		taskSummary := fmt.Sprintf("任务 %d 个", len(tasks))
		if ids := planTaskIDs(tasks, 8); len(ids) > 0 {
			taskSummary += "（" + strings.Join(ids, "、")
			if len(tasks) > len(ids) {
				taskSummary += fmt.Sprintf("、… +%d", len(tasks)-len(ids))
			}
			taskSummary += "）"
		}
		details = append(details, taskSummary)
	} else if progress, ok := data["progress"].(map[string]any); ok {
		if total := formatNumber(progress["total"]); total != "" && total != "0" {
			details = append(details, "任务 "+total+" 个")
		}
	}
	if ready, exists := data["ready"]; exists {
		details = append(details, "可交付 "+formatBool(ready))
	}
	if len(details) == 0 {
		return "Plan " + planID + " updated."
	}
	return "Plan " + planID + "：" + strings.Join(details, "，") + "。"
}

func planTaskIDs(tasks []any, limit int) []string {
	if limit <= 0 {
		return nil
	}
	ids := make([]string, 0, minInt(len(tasks), limit))
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		id, _ := task["task_id"].(string)
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
			if len(ids) == limit {
				break
			}
		}
	}
	return ids
}

func environmentInspectOutputSummary(data map[string]any) string {
	parts := make([]string, 0, 2)
	if snapshotID, _ := data["snapshot_id"].(string); strings.TrimSpace(snapshotID) != "" {
		parts = append(parts, "环境快照 "+strings.TrimSpace(snapshotID)+" 已保存")
	}
	if toolchains := environmentToolchains(data["toolchains"]); len(toolchains) > 0 {
		parts = append(parts, "工具链："+strings.Join(toolchains, "，"))
	}
	if len(parts) == 0 {
		return "环境检查已完成。"
	}
	return strings.Join(parts, "；") + "。"
}

func environmentToolchains(value any) []string {
	toolchains, _ := value.(map[string]any)
	if len(toolchains) == 0 {
		return nil
	}
	preferred := []string{"python", "go", "git", "node", "java"}
	keys := make([]string, 0, len(toolchains))
	seen := make(map[string]bool, len(toolchains))
	for _, key := range preferred {
		if _, exists := toolchains[key]; exists {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	extra := make([]string, 0, len(toolchains)-len(keys))
	for key := range toolchains {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)

	result := make([]string, 0, 3)
	for _, key := range keys {
		if len(result) >= 3 {
			break
		}
		info, _ := toolchains[key].(map[string]any)
		available, _ := info["available"].(bool)
		if !available {
			continue
		}
		version, _ := info["version"].(string)
		version = compactLine(version)
		if version == "" {
			result = append(result, key)
			continue
		}
		result = append(result, key+" "+version)
	}
	return result
}

func runtimeInspectOutputSummary(data map[string]any) string {
	parts := make([]string, 0, 3)
	if stacks := stringValues(data["stacks"], 3); len(stacks) > 0 {
		parts = append(parts, "技术栈："+strings.Join(stacks, "、"))
	}
	if manifests := stringValues(data["manifests"], 3); len(manifests) > 0 {
		parts = append(parts, "清单："+strings.Join(manifests, "、"))
	}
	if status, _ := data["git_status"].(string); strings.TrimSpace(status) != "" {
		parts = append(parts, "Git："+compactLine(status))
	}
	if len(parts) > 0 {
		return "项目摘要：" + strings.Join(parts, "；") + "。"
	}
	if tools := collectionLength(data["tools"]); tools > 0 {
		return fmt.Sprintf("运行时能力已读取：%d 个工具。", tools)
	}
	if documents := instructionCount(data["instructions"]); documents > 0 {
		return fmt.Sprintf("Agent 指令已读取：%d 份文档。", documents)
	}
	return "运行时信息已读取。"
}

func workspaceStateOutputSummary(data map[string]any) string {
	if _, exists := data["items"]; exists {
		returned := collectionLength(data["items"])
		total := formatNumber(data["total"])
		if total == "" {
			total = "0"
		}
		summary := fmt.Sprintf("项目记忆：返回 %d 条，共 %s 条", returned, total)
		if hasMore, _ := data["has_more"].(bool); hasMore {
			summary += "，还有更多"
		}
		return summary + "。"
	}
	if snapshotID, _ := data["snapshot_id"].(string); strings.TrimSpace(snapshotID) != "" {
		files := ""
		if stats, _ := data["stats"].(map[string]any); stats != nil {
			files = formatNumber(stats["files"])
		}
		if files == "" {
			return "文件快照 " + strings.TrimSpace(snapshotID) + " 已创建。"
		}
		return "文件快照 " + strings.TrimSpace(snapshotID) + " 已创建：" + files + " 个文件。"
	}
	if changes, exists := data["changes"]; exists {
		return fmt.Sprintf("文件变更对比完成：%d 项。", collectionLength(changes))
	}
	return ""
}

func screenshotCaptureOutputSummary(data map[string]any) string {
	width := formatNumber(data["output_width"])
	height := formatNumber(data["output_height"])
	format, _ := data["format"].(string)
	display := formatNumber(data["display"])
	parts := make([]string, 0, 3)
	if width != "" && height != "" {
		parts = append(parts, width+"×"+height)
	}
	if strings.TrimSpace(format) != "" {
		parts = append(parts, strings.TrimSpace(format))
	}
	if display != "" {
		parts = append(parts, "显示器 "+display)
	}
	if len(parts) == 0 {
		return "截图已捕获。"
	}
	return "截图已捕获：" + strings.Join(parts, "，") + "。"
}

func stringValues(value any, limit int) []string {
	items, _ := value.([]any)
	if len(items) == 0 {
		return nil
	}
	result := make([]string, 0, minInt(len(items), limit))
	for _, item := range items {
		text, _ := item.(string)
		if text = strings.TrimSpace(text); text != "" {
			result = append(result, text)
			if len(result) == limit {
				break
			}
		}
	}
	return result
}

func collectionLength(value any) int {
	if items, ok := value.([]any); ok {
		return len(items)
	}
	return 0
}

func instructionCount(value any) int {
	instructions, _ := value.(map[string]any)
	return collectionLength(instructions["documents"])
}

func formatBool(value any) string {
	if value, ok := value.(bool); ok {
		if value {
			return "是"
		}
		return "否"
	}
	return formatNumber(value)
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
		if summary := errorEnvelopeSummary(object); summary != "" {
			return summary
		}
		return compactMap(object)
	}
	return fmt.Sprint(value)
}

var humanProtocolFields = map[string]struct{}{
	"completed_at":       {},
	"completed_at_ms":    {},
	"network_latency_ms": {},
	"processing_ms":      {},
	"received_at":        {},
	"received_at_ms":     {},
	"remote_session_id":  {},
	"request_id":         {},
	"server_elapsed_ms":  {},
	"started_at":         {},
	"started_at_ms":      {},
	"status":             {},
	"timing":             {},
	"ok":                 {},
}

func compactMap(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, diagnostic := humanProtocolFields[key]; diagnostic {
			continue
		}
		if formatted := compactValue(value[key]); formatted != "" {
			parts = append(parts, key+"="+formatted)
		}
	}
	return strings.Join(parts, " ")
}

func errorEnvelopeSummary(value map[string]any) string {
	raw, exists := value["error"]
	if !exists || raw == nil {
		return ""
	}
	switch details := raw.(type) {
	case string:
		return compactLine(details)
	case map[string]any:
		return formatErrorDetails(details)
	default:
		return "operation failed"
	}
}

func formatErrorDetails(details map[string]any) string {
	code, _ := details["code"].(string)
	message, _ := details["message"].(string)
	code = strings.TrimSpace(code)
	message = compactLine(message)
	switch {
	case code != "" && message != "":
		return compactLine(code + ": " + message)
	case message != "":
		return message
	case code != "":
		return code
	default:
		return compactMap(details)
	}
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
