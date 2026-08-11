package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func renderToolCompleted(w io.Writer, event Event, options renderOptions, suppressCommandOutput bool) error {
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	verb, label := toolAction(event.Tool, event.Input)
	if isCommandTool(event.Tool) && strings.TrimSpace(event.Command) != "" {
		label = compactCommand(event.Command)
	}
	if isFileReadInput(event.Tool, event.Input) && label == "files" {
		if result, ok := payload["result"].(map[string]any); ok {
			if outputLabel := fileReadResultLabel(result); outputLabel != "" {
				label = outputLabel
			}
		}
	}
	status, _ := payload["status"].(string)
	failed, failureMessage := toolFailure(payload)
	if isProgressTool(event.Tool) && !failed {
		return renderProgressCompleted(w, event, options)
	}
	if failed {
		verb = failureActionVerb(event.Tool, verb)
	}
	if !options.suppressAction {
		if isCommandTool(event.Tool) {
			if err := writeCommandAction(w, event, verb, label, actionColor(event.Tool, failed, options.colorMode), options.colorMode); err != nil {
				return err
			}
		} else if err := writeEventAction(w, event, verb, label, actionColor(event.Tool, failed, options.colorMode), options.colorMode); err != nil {
			return err
		}
	}
	if readItems := fileReadDetailLines(event.Tool, event.Input); len(readItems) > 0 {
		if err := writeChildren(w, readItems, options.colorMode); err != nil {
			return err
		}
	}

	details := make([]string, 0, 2)
	if facts := eventFactLine(event, options.detail, options.suppressDuration); facts != "" {
		details = append(details, facts)
	}
	if failed {
		if failureMessage == "" {
			failureMessage = errorSummary(payload)
		}
		if message := failureMessage; message != "" {
			if detail := failureDisplay(message); detail != "" {
				details = append(details, detail)
			}
		}
		if len(details) == 0 && status != "" && status != "succeeded" {
			details = append(details, strings.ReplaceAll(status, "_", " "))
		}
		return writeChildren(w, details, options.colorMode)
	}
	if len(details) > 0 {
		if err := writeChildren(w, details, options.colorMode); err != nil {
			return err
		}
	}
	wroteDetail := len(details) > 0
	if !options.suppressContext && hasSemanticContext(event) {
		if strings.TrimSpace(event.Purpose) != "" {
			if err := renderPurposeBanner(w, event, options.colorMode); err != nil {
				return err
			}
			wroteDetail = true
		}
		contextLines := semanticContextLines(event, options.detail)
		if len(contextLines) > 0 {
			if err := writeChildren(w, contextLines, options.colorMode); err != nil {
				return err
			}
			wroteDetail = true
		}
	}
	if result, ok := payload["result"].(map[string]any); ok {
		if output := humanToolOutput(event.Tool, result); output != "" {
			if suppressCommandOutput && (event.Tool == "command_execute" || event.Tool == "command_run") {
				output = commandCompletionSummary(output)
			}
			if err := writeChild(w, output, options.colorMode); err != nil {
				return err
			}
			wroteDetail = true
		}
	}
	if !wroteDetail && status != "" && status != "succeeded" {
		return writeChild(w, strings.ReplaceAll(status, "_", " "), options.colorMode)
	}
	return nil
}

func writeEventAction(w io.Writer, event Event, verb, label, fallbackColor string, mode ColorMode) error {
	verb = sanitizeTerminalText(verb)
	label = sanitizeTerminalText(label)
	if label == "" {
		label = "operation"
	}
	color := mode != ColorModeNone
	statusColor := eventActionColor(event, fallbackColor, mode)
	// Marker carries the status color; the verb keeps the tool color; the label
	// stays in the default foreground so the action line has an obvious hierarchy.
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(eventMarker(event), statusColor, color), paint(verb, fallbackColor, color), label)
	return err
}

func writeCommandAction(w io.Writer, event Event, verb, command, fallbackColor string, mode ColorMode) error {
	verb = sanitizeTerminalText(verb)
	command = sanitizeTerminalText(command)
	if command == "" {
		command = "command"
	}
	color := mode != ColorModeNone
	statusColor := eventActionColor(event, fallbackColor, mode)
	// Marker/status and verb/tool keep their semantic colors; the command text is
	// the primary content and stays in the default foreground.
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(eventMarker(event), statusColor, color), paint(verb, fallbackColor, color), command)
	return err
}

func writeEventActionWithDiffStats(w io.Writer, event Event, verb, label, fallbackColor string, added, removed int, mode ColorMode) error {
	verb = sanitizeTerminalText(verb)
	label = sanitizeTerminalText(label)
	if label == "" {
		label = "operation"
	}
	label = styleCompactDiffStats(label, added, removed, mode)
	color := mode != ColorModeNone
	statusColor := eventActionColor(event, fallbackColor, mode)
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(eventMarker(event), statusColor, color), paint(verb, fallbackColor, color), label)
	return err
}

func commandStreamColor(stream string) string {
	if strings.EqualFold(strings.TrimSpace(stream), "stderr") {
		return ansiYellow
	}
	if strings.EqualFold(strings.TrimSpace(stream), "stdout") {
		return ansiGray
	}
	return ansiAmber
}

func writeCommandStreamHeader(w io.Writer, stream string, mode ColorMode) error {
	stream = strings.TrimSpace(sanitizeTerminalText(stream))
	if stream == "" {
		stream = "output"
	}
	color := mode != ColorModeNone
	colorCode := commandStreamColor(stream)
	_, err := fmt.Fprintf(w, "  %s\n", paint(stream+":", colorCode, color))
	return err
}

func writeChildren(w io.Writer, values []string, mode ColorMode) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := writeChild(w, value, mode); err != nil {
			return err
		}
	}
	return nil
}

func writeChild(w io.Writer, value string, mode ColorMode) error {
	// Preserve full model-authored text (progress_summary / purpose notes).
	// Only collapse to a single logical line when the value is already one line;
	// multi-line notes keep every line (indented under the first fact line).
	value = strings.TrimRight(sanitizeTerminalText(value), "\r\n")
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if i == 0 {
			if _, err := fmt.Fprintf(w, "  %s\n", styleFactLine(line, mode)); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "    %s\n", styleFactLine(line, mode)); err != nil {
			return err
		}
	}
	return nil
}

// factLabelKeys are the labels that may be tinted with the muted color inside
// fact and context lines. Everything else (including error prose) is left
// untouched so failure messages never lose their plain-text meaning.
var factLabelKeys = map[string]struct{}{
	"time": {}, "exit": {}, "path": {}, "size": {}, "stdout": {}, "stderr": {},
	"exit_code": {}, "results": {}, "items": {}, "purpose": {}, "progress": {},
	"next": {}, "reasoning": {}, "plan": {}, "plan task": {}, "execution task": {},
	"operation": {}, "cwd": {}, "command": {}, "phase": {}, "call": {}, "skill": {},
	"mcp": {}, "status": {}, "code": {}, "role": {}, "name": {}, "sha256": {},
	"confirmation_digest": {}, "deletions": {}, "workspaces": {}, "tool": {},
}

// styleFactLine tints known fact labels (time=..., path=..., purpose: ...) with
// the muted secondary color while keeping values and unknown text untouched.
func styleFactLine(value string, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	muted := mutedColor(mode)
	var builder strings.Builder
	for index, part := range strings.Split(value, " · ") {
		if index > 0 {
			builder.WriteString(" · ")
		}
		builder.WriteString(styleFactPart(part, muted))
	}
	return builder.String()
}

func styleFactPart(part, muted string) string {
	// Match "key: value", "key=value", then "key value" (time 12ms, exit 0).
	// The plain-space match is lowest priority so quoted values like
	// path="main.go" size=1024 are not split at an interior space.
	for _, separator := range []string{": ", "=", " "} {
		if cut := strings.Index(part, separator); cut > 0 {
			key := strings.TrimSpace(part[:cut])
			if _, ok := factLabelKeys[key]; ok {
				return muted + part[:cut] + ansiReset + part[cut:]
			}
		}
	}
	return part
}

func writeChildWithDiffStats(w io.Writer, value string, added, removed int, mode ColorMode) error {
	value = strings.TrimRight(sanitizeTerminalText(value), "\r\n")
	if value == "" {
		return nil
	}
	value = styleCompactDiffStats(value, added, removed, mode)
	_, err := fmt.Fprintf(w, "  %s\n", value)
	return err
}

func writeCommandOutputLine(w io.Writer, value, stream string, options renderOptions) error {
	value = compactCodeLine(sanitizeTerminalText(value))
	// The rendered line is "    " + "%3d | " + value. flushBodyLine measures
	// the leading whitespace as 6 columns (4 indent + 2 number padding), so its
	// continuation indent is 8 and its body budget is terminalWidth - 8. Keep
	// the whole line within that budget: 4 + 8 = 12 columns of overhead, so the
	// trailing ellipsis is never wrapped onto its own line.
	if width := options.terminalWidth - 12; width > 0 {
		value = truncateRenderedLine(value, width)
	}
	// Split the "%3d | " line-number prefix so it can use the muted secondary
	// color while the payload keeps its stream color.
	number := ""
	if cut := strings.Index(value, " | "); cut > 0 {
		number = value[:cut+3]
		value = value[cut+3:]
	}
	if options.colorMode != ColorModeNone {
		switch strings.ToLower(strings.TrimSpace(stream)) {
		case "stdout":
			// Match diff context: low-emphasis output that stays readable without
			// competing with the RUN action itself.
			value = ansiDim + value + ansiReset
		case "stderr":
			value = ansiYellow + value + ansiReset
		default:
			value = paint(value, commandStreamColor(stream), true)
		}
		if number != "" {
			number = paint(number, mutedColor(options.colorMode), true)
		}
	}
	_, err := fmt.Fprintf(w, "    %s%s\n", number, value)
	return err
}

func writeCodeChild(w io.Writer, value string, options renderOptions, width int) error {
	value = compactCodeLine(sanitizeTerminalText(value))
	if width > 0 {
		value = formatDiffLine(value, options.colorMode, width)
	} else {
		value = diffLineStyle(value, options.colorMode)
	}
	_, err := fmt.Fprintf(w, "    %s\n", value)
	return err
}

func toolAction(tool string, raw []byte) (string, string) {
	input := inputMap(raw)
	action, _ := input["action"].(string)
	if strings.TrimSpace(action) == "" {
		for _, key := range []string{"view", "operation", "transition"} {
			if value, ok := input[key].(string); ok {
				action = value
				break
			}
		}
	}
	action = strings.ToLower(strings.TrimSpace(action))
	verb := actionVerb(tool, action)
	label := ""

	// Special case: skill/MCP calls via extension_manage (or skill_execute) should display
	// the skill/MCP name in terminal observation instead of "extension_manage".
	if (tool == "extension_manage" || tool == "skill_execute" || tool == "skill_call" || tool == "mcp_call") && (action == "call" || tool == "skill_call" || tool == "mcp_call") {
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
	case "execute", "command_execute", "command_run":
		label, _ = input["command"].(string)
		if strings.TrimSpace(label) == "" {
			label, _ = input["task"].(string)
		}
		label = compactCommand(label)
	case "read":
		label = readActionLabel(input)
	case "file_read":
		label = fileReadLabel(input)
	case "context_query":
		label = contextQueryCommand(input)
	case "source_read":
		if input["view"] == "file" {
			label = fileReadLabel(input)
		} else {
			label = contextQueryCommand(map[string]any{"action": input["view"], "query": input["query"], "paths": input["paths"], "include_glob": input["include_glob"], "exclude_glob": input["exclude_glob"]})
		}
	case "progress":
		label, _ = input["current"].(string)
	case "workspace_list":
		label = "workspaces"
	case "session_open":
		label, _ = input["workspace"].(string)
		if strings.TrimSpace(label) == "" {
			label, _ = input["remote_session_id"].(string)
		}
		if strings.TrimSpace(label) == "" {
			label, _ = input["session_id"].(string)
		}
	case "runtime_inspect":
		label = runtimeInspectLabel(action)
	case "runtime_read":
		label = runtimeInspectLabel(stringValue(input["view"]))
	case "workspace_state":
		label = workspaceStateLabel(action)
	case "workspace_observe":
		label = workspaceStateLabel(stringValue(input["view"]))
	case "screenshot_capture":
		label = "screenshot"
	case "skill_call":
		label = stringValue(input["name"])
	case "mcp_call":
		label = stringValue(input["server"])
		if toolName := stringValue(input["tool"]); toolName != "" {
			label += "/" + toolName
		}
	default:
		for _, key := range []string{"workspace", "path", "plan_task_id", "execution_task_id", "artifact_id", "remote_session_id"} {
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
	case "execute", "command_execute", "command_run":
		return "Ran"
	case "context_query":
		return "Searched"
	case "read", "file_read":
		return "Read"
	case "artifact_register":
		return "Edited"
	case "session_open":
		return "Opened"
	case "workspace_list":
		return "Listed"
	case "progress":
		return "Progress"
	case "session_transition":
		return "Updated"
	case "screenshot_capture":
		return "Captured"
	case "runtime_inspect":
		return "Read"
	case "workspace_state":
		if action == "snapshot" {
			return "Created"
		}
		return "Read"
	case "workspace_observe", "workspace_history_read", "session_read", "source_read", "task_read", "plan_read", "runtime_read", "environment_read", "artifact_read", "extension_discover":
		return "Read"
	case "task_control":
		return "Controlled"
	case "plan_create", "environment_snapshot_create":
		return "Created"
	case "plan_transition":
		return "Updated"
	case "skill_call", "mcp_call":
		return "Called"
	case "secret_provide":
		return "Provided"
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

func isCommandTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "execute", "command_execute", "command_run":
		return true
	default:
		return false
	}
}
