package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
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
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode, diffMode: DiffModeFull}, false)
}

func renderText(w io.Writer, event Event, color, suppressCommandOutput bool) error {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode, diffMode: DiffModeFull}, suppressCommandOutput)
}

type renderOptions struct {
	colorMode            ColorMode
	terminalWidth        int
	detail               bool
	diffMode             DiffMode
	diffCache            *diffDocumentCache
	suppressAction       bool
	suppressOutputAction bool
	commandOutputStarted bool
	suppressContext      bool
	suppressDuration     bool
	outputLineStart      int
}

func renderTextWithOptions(w io.Writer, event Event, options renderOptions, suppressCommandOutput bool) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	switch event.Type {
	case TypeToolStarted:
		if !hasSemanticContext(event) {
			return nil
		}
		return renderProgressSummary(w, event, options)
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
		summary := event.Summary
		if strings.TrimSpace(summary) == "" {
			summary = event.Type
		}
		return renderSummaryEvent(w, event, "Observed", summary, event.Output, options.colorMode != ColorModeNone)
	}
}

// RenderJSON writes one complete JSON event per line for scripts and log
// ingestion. It intentionally does not wrap the event in a protocol frame.
func RenderJSON(w io.Writer, event Event) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	event.SetDefaults()
	return json.NewEncoder(w).Encode(event)
}

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
			if err := writeCommandAction(w, event, verb, label, actionColor(event.Tool, failed), options.colorMode != ColorModeNone); err != nil {
				return err
			}
		} else if err := writeEventAction(w, event, verb, label, actionColor(event.Tool, failed), options.colorMode != ColorModeNone); err != nil {
			return err
		}
	}
	if readItems := fileReadDetailLines(event.Tool, event.Input); len(readItems) > 0 {
		if err := writeChildren(w, readItems, options.colorMode != ColorModeNone); err != nil {
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
		return writeChildren(w, details, options.colorMode != ColorModeNone)
	}
	if len(details) > 0 {
		if err := writeChildren(w, details, options.colorMode != ColorModeNone); err != nil {
			return err
		}
	}
	wroteDetail := len(details) > 0
	if !options.suppressContext && hasSemanticContext(event) {
		contextLines := semanticContextLines(event, options.detail)
		if len(contextLines) > 0 {
			if err := writeChildren(w, contextLines, options.colorMode != ColorModeNone); err != nil {
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
			if err := writeChild(w, output, options.colorMode != ColorModeNone); err != nil {
				return err
			}
			wroteDetail = true
		}
	}
	if !wroteDetail && status != "" && status != "succeeded" {
		return writeChild(w, strings.ReplaceAll(status, "_", " "), options.colorMode != ColorModeNone)
	}
	return nil
}

func renderProgressSummary(w io.Writer, event Event, options renderOptions) error {
	return writeChildren(w, semanticContextLines(event, options.detail), options.colorMode != ColorModeNone)
}

type progressView struct {
	Status      string
	Current     string
	Result      string
	Next        string
	Phase       string
	RelatedTool string
}

func isProgressTool(tool string) bool {
	return strings.EqualFold(strings.TrimSpace(tool), "progress")
}

func progressEventView(event Event) progressView {
	input := inputMap(event.Input)
	view := progressView{
		Status:      strings.ToLower(firstProgressString(input, "status")),
		Current:     firstProgressString(input, "current"),
		Result:      firstProgressString(input, "result"),
		Next:        firstProgressString(input, "next"),
		Phase:       firstProgressString(input, "phase"),
		RelatedTool: firstProgressString(input, "related_tool"),
	}
	if view.Status == "" {
		view.Status = "in_progress"
	}
	if view.Current == "" {
		view.Current = strings.TrimSpace(event.ProgressSummary)
	}
	return view
}

func firstProgressString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(input[key]); value != "" {
			return value
		}
	}
	return ""
}

func renderProgressCompleted(w io.Writer, event Event, options renderOptions) error {
	view := progressEventView(event)
	if view.Current == "" {
		view.Current = "progress update"
	}
	marker, verb, color := progressAppearance(view.Status)
	if err := writeProgressAction(w, marker, verb, view.Current, color, options.colorMode != ColorModeNone); err != nil {
		return err
	}
	details := make([]string, 0, 4)
	if view.Result != "" {
		details = append(details, "result: "+view.Result)
	}
	if view.Next != "" {
		details = append(details, "next: "+view.Next)
	}
	if options.detail && view.Phase != "" {
		details = append(details, "phase: "+view.Phase)
	}
	if options.detail && view.RelatedTool != "" {
		details = append(details, "related tool: "+view.RelatedTool)
	}
	return writeChildren(w, details, options.colorMode != ColorModeNone)
}

func progressAppearance(status string) (marker, verb, color string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return "✓", "Done", ansiGreen
	case "waiting_for_user":
		return "?", "Waiting", ansiYellow
	case "blocked":
		return "!", "Blocked", ansiYellow
	case "failed":
		return "✗", "Failed", ansiRed
	default:
		return "◆", "Progress", ansiCyan
	}
}

func writeProgressAction(w io.Writer, marker, verb, current, color string, enabled bool) error {
	marker = sanitizeTerminalText(marker)
	verb = sanitizeTerminalText(verb)
	current = strings.TrimSpace(sanitizeTerminalText(current))
	if current == "" {
		current = "progress update"
	}
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(marker, color, enabled), paint(verb, color, enabled), current)
	return err
}

func hasSemanticContext(event Event) bool {
	return strings.TrimSpace(event.Purpose) != "" || strings.TrimSpace(event.ReasoningSummary) != "" ||
		strings.TrimSpace(event.ProgressSummary) != "" || strings.TrimSpace(event.NextStep) != "" ||
		strings.TrimSpace(event.PlanID) != "" || strings.TrimSpace(event.PlanTaskID) != "" ||
		strings.TrimSpace(event.ExecutionTaskID) != ""
}

func semanticContextLines(event Event, detail bool) []string {
	groups := []semanticContextGroup{
		{
			{label: "purpose", value: event.Purpose},
			{label: "progress", value: event.ProgressSummary},
			{label: "next", value: event.NextStep},
		},
		{
			{label: "reasoning", value: event.ReasoningSummary},
			{label: "plan", value: event.PlanID},
			{label: "plan task", value: event.PlanTaskID},
			{label: "execution task", value: event.ExecutionTaskID},
		},
	}
	if detail {
		groups[1] = append(groups[1], semanticContextField{label: "operation", value: event.OperationID})
	}
	return semanticContextGroups(groups)
}

type semanticContextField struct {
	label string
	value string
}

type semanticContextGroup []semanticContextField

func semanticContextGroups(groups []semanticContextGroup) []string {
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		parts := make([]string, 0, len(group))
		for _, field := range group {
			if value := compactLine(field.value); value != "" {
				parts = append(parts, field.label+": "+value)
			}
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " · "))
		}
	}
	return lines
}

func renderCommandOutput(w io.Writer, event Event, options renderOptions) error {
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	text, _ := payload["text"].(string)
	text = sanitizeTerminalText(text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	stream := strings.TrimSpace(event.Stream)
	if stream == "" {
		stream = "output"
	}
	command := strings.TrimSpace(event.Command)
	if command != "" && !options.commandOutputStarted {
		if err := writeCommandAction(w, event, "Ran", compactCommand(command), actionColor(event.Tool, false), options.colorMode != ColorModeNone); err != nil {
			return err
		}
	}
	if !options.suppressOutputAction {
		if command != "" {
			if err := writeCommandStreamHeader(w, stream, options.colorMode != ColorModeNone); err != nil {
				return err
			}
		} else {
			if err := writeEventAction(w, event, "Read", stream, commandStreamColor(event.Stream), options.colorMode != ColorModeNone); err != nil {
				return err
			}
		}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		lineNumber := options.outputLineStart + i
		if lineNumber <= 0 {
			lineNumber = i + 1
		}
		prefixed := fmt.Sprintf("%3d | %s", lineNumber, line)
		if err := writeCommandOutputLine(w, prefixed, stream, options); err != nil {
			return err
		}
	}
	return nil
}

func renderFileChanged(w io.Writer, event Event, options renderOptions) error {
	if strings.EqualFold(strings.TrimSpace(event.Tool), "move_out") {
		if handled, err := renderMoveOutChanged(w, event, options); handled || err != nil {
			return err
		}
	}
	var payload struct {
		Results []fileChangeView `json:"results"`
	}
	if err := json.Unmarshal(event.Output, &payload); err != nil {
		payload.Results = nil
	}
	files := payload.Results
	if len(files) == 0 {
		label := compactLine(event.Summary)
		if label == "" {
			label = "files"
		}
		if err := writeEventAction(w, event, "Changed", label, ansiGreen, options.colorMode != ColorModeNone); err != nil {
			return err
		}
		return writeChildren(w, []string{"file details unavailable"}, options.colorMode != ColorModeNone)
	}
	label := fmt.Sprintf("%d files", len(files))
	if len(files) == 1 {
		label = files[0].Path
	}
	added, removed := 0, 0
	for _, file := range files {
		document := options.diffCache.get(file.Diff)
		added += document.added
		removed += document.removed
	}
	if added > 0 || removed > 0 {
		label += formatCompactDiffStats(added, removed)
	}
	if len(files) == 1 {
		if format := fileFormatSummary(files[0]); format != "" {
			label += " [" + format + "]"
		}
	}
	if err := writeEventActionWithDiffStats(w, event, fileChangeVerb(files), label, actionColor(event.toolOrType(), false), added, removed, options.colorMode); err != nil {
		return err
	}
	for index, file := range files {
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
		document := options.diffCache.get(file.Diff)
		stats := document.stats()
		// The single-file action line already contains the path and aggregate
		// stats. Repeating it as a child line makes large edits look noisy.
		if len(files) == 1 {
			if file.Diff != "" && options.diffMode != DiffModeSummary {
				document := options.diffCache.get(file.Diff)
				limit := 0
				if options.diffMode == DiffModePreview {
					limit = defaultDiffPreviewLines
				}
				truncated, err := renderDiffDocument(w, document, options, limit)
				if err != nil {
					return err
				}
				if truncated || file.DiffTruncated {
					if err := writeChild(w, "diff preview truncated; use -diff full for complete output", options.colorMode != ColorModeNone); err != nil {
						return err
					}
				}
			}
			continue
		}
		line := path
		if file.Operation != "" {
			line += " (" + file.Operation + ")"
		}
		if stats != "" {
			line += " " + formatCompactDiffStats(document.added, document.removed)
		}
		if format := fileFormatSummary(file); format != "" {
			line += " [" + format + "]"
		}
		if err := writeChildWithDiffStats(w, line, document.added, document.removed, options.colorMode); err != nil {
			return err
		}
		if file.Diff != "" && options.diffMode != DiffModeSummary {
			limit := 0
			if options.diffMode == DiffModePreview {
				limit = defaultDiffPreviewLines
			}
			truncated, err := renderDiffDocument(w, document, options, limit)
			if err != nil {
				return err
			}
			if truncated || file.DiffTruncated {
				if err := writeChild(w, "diff preview truncated; use -diff full for complete output", options.colorMode != ColorModeNone); err != nil {
					return err
				}
			}
		}
	}
	if len(files) > maxChangedFiles {
		if err := writeChild(w, fmt.Sprintf("... and %d more files", len(files)-maxChangedFiles), options.colorMode != ColorModeNone); err != nil {
			return err
		}
	}
	return nil
}

func renderMoveOutChanged(w io.Writer, event Event, options renderOptions) (bool, error) {
	var payload struct {
		Status                 string `json:"status"`
		MovedCount             int    `json:"moved_count"`
		FailedCount            int    `json:"failed_count"`
		TargetCount            int    `json:"target_count"`
		TargetPreviewTruncated bool   `json:"target_preview_truncated"`
		Reversible             bool   `json:"reversible"`
		TargetPreview          []struct {
			Path           string `json:"path"`
			Status         string `json:"status"`
			QuarantinePath string `json:"quarantine_path"`
			ErrorCode      string `json:"error_code"`
		} `json:"target_preview"`
	}
	if json.Unmarshal(event.Output, &payload) != nil || len(payload.TargetPreview) == 0 {
		return false, nil
	}
	total := payload.TargetCount
	if total <= 0 {
		total = len(payload.TargetPreview)
	}
	verb := "Removed"
	label := fmt.Sprintf("%d targets", total)
	if total == 1 {
		label = payload.TargetPreview[0].Path
	}
	if payload.MovedCount == 0 && payload.FailedCount > 0 {
		verb = "Move failed"
	} else if payload.FailedCount > 0 {
		label = fmt.Sprintf("%d of %d targets", payload.MovedCount, total)
	}
	if err := writeEventAction(w, event, verb, label, ansiGreen, options.colorMode != ColorModeNone); err != nil {
		return true, err
	}
	for _, target := range payload.TargetPreview {
		path := strings.TrimSpace(target.Path)
		if path == "" {
			path = "target"
		}
		status := strings.ToLower(strings.TrimSpace(target.Status))
		line := ""
		switch status {
		case "moved", "committed", "succeeded", "success":
			if total == 1 {
				line = "Moved to quarantine"
			} else {
				line = path + " — moved to quarantine"
			}
		default:
			if total == 1 {
				line = "Move failed"
			} else {
				line = path + " — move failed"
			}
			if code := strings.TrimSpace(target.ErrorCode); code != "" {
				line += ": " + code
			}
		}
		if err := writeChild(w, line, options.colorMode != ColorModeNone); err != nil {
			return true, err
		}
	}
	if payload.TargetPreviewTruncated {
		if err := writeChild(w, fmt.Sprintf("... and %d more targets", total-len(payload.TargetPreview)), options.colorMode != ColorModeNone); err != nil {
			return true, err
		}
	}
	summary := fmt.Sprintf("Moved %d · failed %d", payload.MovedCount, payload.FailedCount)
	if payload.Reversible {
		summary += " · reversible"
	} else {
		summary += " · not reversible"
	}
	if err := writeChild(w, summary, options.colorMode != ColorModeNone); err != nil {
		return true, err
	}
	return true, nil
}

func formatCompactDiffStats(added, removed int) string {
	return fmt.Sprintf(" [-%d,+%d]", removed, added)
}

func styleCompactDiffStats(value string, added, removed int, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	plain := formatCompactDiffStats(added, removed)
	styled := " [" +
		paint(fmt.Sprintf("-%d", removed), diffRemovedForeground(mode), true) + "," +
		paint(fmt.Sprintf("+%d", added), diffAddedForeground(mode), true) + "]"
	return strings.Replace(value, plain, styled, 1)
}

type fileChangeView struct {
	Path            string         `json:"path"`
	NewPath         string         `json:"new_path"`
	Operation       string         `json:"operation"`
	Diff            string         `json:"diff"`
	DiffTruncated   bool           `json:"diff_truncated"`
	OriginalFormat  fileFormatView `json:"original_format"`
	ProposedFormat  fileFormatView `json:"proposed_format"`
	FormatPreserved bool           `json:"format_preserved"`
}

func fileChangeVerb(files []fileChangeView) string {
	if len(files) == 0 {
		return "Changed"
	}
	verb := ""
	for _, file := range files {
		current := map[string]string{
			"create": "Created", "update": "Edited", "delete": "Deleted", "rename": "Renamed",
		}[strings.ToLower(strings.TrimSpace(file.Operation))]
		if current == "" {
			current = "Changed"
		}
		if verb == "" {
			verb = current
			continue
		}
		if verb != current {
			return "Changed"
		}
	}
	return verb
}

func fileFormatSummary(file fileChangeView) string {
	original := formatViewSummary(file.OriginalFormat)
	proposed := formatViewSummary(file.ProposedFormat)
	if original == "" {
		return proposed
	}
	if proposed == "" || original == proposed {
		return original
	}
	return original + " -> " + proposed
}

func formatViewSummary(format fileFormatView) string {
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(format.Charset); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(format.BOM); value != "" && value != "none" {
		parts = append(parts, "BOM "+value)
	}
	if value := strings.TrimSpace(format.LineEnding); value != "" && value != "none" {
		parts = append(parts, value)
	}
	if format.FinalNewline != nil {
		if *format.FinalNewline {
			parts = append(parts, "final newline")
		} else {
			parts = append(parts, "no final newline")
		}
	}
	return strings.Join(parts, ", ")
}

const (
	// Labels (verb + path/query) may be long absolute paths; do not ellipsis early.
	maxToolSummaryRunes     = 2048
	maxChangedFiles         = 50
	defaultDiffPreviewLines = 40
)

type fileFormatView struct {
	Charset      string `json:"charset"`
	BOM          string `json:"bom"`
	LineEnding   string `json:"line_ending"`
	FinalNewline *bool  `json:"final_newline"`
}

func writeEventAction(w io.Writer, event Event, verb, label, fallbackColor string, color bool) error {
	verb = sanitizeTerminalText(verb)
	label = sanitizeTerminalText(label)
	if label == "" {
		label = "operation"
	}
	colorCode := eventActionColor(event, fallbackColor)
	marker := eventMarker(event)
	// Keep model-supplied path/query labels intact; the timeline wraps long rows.
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(marker, colorCode, color), paint(verb, colorCode, color), label)
	return err
}

func writeCommandAction(w io.Writer, event Event, verb, command, fallbackColor string, color bool) error {
	verb = sanitizeTerminalText(verb)
	command = sanitizeTerminalText(command)
	if command == "" {
		command = "command"
	}
	colorCode := eventActionColor(event, fallbackColor)
	marker := eventMarker(event)
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(marker, colorCode, color), paint(verb, colorCode, color), paint(command, colorCode, color))
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
	colorCode := eventActionColor(event, fallbackColor)
	marker := eventMarker(event)
	_, err := fmt.Fprintf(w, "%s %s %s\n", paint(marker, colorCode, color), paint(verb, colorCode, color), label)
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

func writeCommandStreamHeader(w io.Writer, stream string, color bool) error {
	stream = strings.TrimSpace(sanitizeTerminalText(stream))
	if stream == "" {
		stream = "output"
	}
	colorCode := commandStreamColor(stream)
	_, err := fmt.Fprintf(w, "  %s %s\n", paint("↳", colorCode, color), paint(stream+":", colorCode, color))
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
	// Preserve full model-authored text (progress_summary / purpose notes).
	// Only collapse to a single logical line when the value is already one line;
	// multi-line notes keep every line (indented under the first ↳).
	value = strings.TrimRight(sanitizeTerminalText(value), "\r\n")
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if i == 0 {
			if _, err := fmt.Fprintf(w, "  %s %s\n", paint("↳", ansiBlue, color), line); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

func writeChildWithDiffStats(w io.Writer, value string, added, removed int, mode ColorMode) error {
	value = strings.TrimRight(sanitizeTerminalText(value), "\r\n")
	if value == "" {
		return nil
	}
	value = styleCompactDiffStats(value, added, removed, mode)
	_, err := fmt.Fprintf(w, "  %s %s\n", paint("↳", ansiBlue, mode != ColorModeNone), value)
	return err
}

func writeCommandOutputLine(w io.Writer, value, stream string, options renderOptions) error {
	value = compactCodeLine(sanitizeTerminalText(value))
	if width := options.terminalWidth - 8; width > 0 {
		value = truncateRenderedLine(value, width)
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
	}
	_, err := fmt.Fprintf(w, "    %s\n", value)
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

func renderSummaryEvent(w io.Writer, event Event, verb, summary string, raw []byte, color bool) error {
	if strings.TrimSpace(summary) == "" {
		summary = "event"
	}
	if err := writeEventAction(w, event, verb, summary, actionColor(event.toolOrType(), false), color); err != nil {
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
	for _, key := range []string{"plan_task_id", "execution_task_id", "status", "exit_code"} {
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

func publicView(raw []byte) string {
	input := inputMap(raw)
	view, _ := input["view"].(string)
	return strings.ToLower(strings.TrimSpace(view))
}

func eventFactLine(event Event, detail, suppressDuration bool) string {
	parts := make([]string, 0, 10)
	command := isCommandTool(event.Tool)
	if tool := strings.TrimSpace(event.Tool); tool != "" {
		if detail {
			parts = append(parts, "tool="+tool)
		} else {
			parts = append(parts, "tool: "+tool)
		}
	}
	if detail && event.Command != "" {
		parts = append(parts, "command="+compactCommand(event.Command))
	}
	if detail && event.WorkingDirectory != "" {
		parts = append(parts, "cwd="+compactLine(event.WorkingDirectory))
	}
	if event.ExitCode != nil {
		if command && !detail {
			parts = append(parts, fmt.Sprintf("exit %d", *event.ExitCode))
		} else {
			parts = append(parts, fmt.Sprintf("exit=%d", *event.ExitCode))
		}
	}
	if event.DurationMs > 0 {
		duration := (time.Duration(event.DurationMs) * time.Millisecond).String()
		switch {
		case !detail:
			parts = append(parts, "time "+duration)
		case !suppressDuration:
			parts = append(parts, fmt.Sprintf("duration=%dms", event.DurationMs))
		}
	}
	if event.SkillName != "" {
		parts = append(parts, "skill="+event.SkillName)
	}
	if event.MCPServer != "" {
		mcp := "mcp=" + event.MCPServer
		if event.MCPTool != "" {
			mcp += "/" + event.MCPTool
		}
		parts = append(parts, mcp)
	}
	if detail && event.Path != "" {
		parts = append(parts, "path="+event.Path)
	}
	if detail && event.Phase != "" {
		parts = append(parts, "phase="+event.Phase)
	}
	if detail && event.CallID != "" {
		parts = append(parts, "call="+event.CallID)
	}
	if detail && event.OperationID != "" {
		parts = append(parts, "operation="+event.OperationID)
	}
	if detail && event.ParentOperationID != "" {
		parts = append(parts, "parent_operation="+event.ParentOperationID)
	}
	if detail && event.StepID != "" {
		parts = append(parts, "step="+event.StepID)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
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

func isCommandTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "execute", "command_execute", "command_run":
		return true
	default:
		return false
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

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func errorSummary(payload map[string]any) string {
	if message, ok := payload["error"].(string); ok {
		if summary := failureSummary(message); summary != "" {
			return summary
		}
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
	status := strings.ToLower(strings.TrimSpace(stringValue(payload["status"])))
	failedStatus := isErrorStatus(status) || status == "denied" || status == "unauthorized"
	if failedStatus {
		if summary := failureSummary(stringValue(payload["summary"])); summary != "" && !strings.EqualFold(summary, status) {
			return true, summary
		}
		return true, errorSummary(payload)
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return false, ""
	}
	isError, _ := payload["is_error"].(bool)
	if message := nestedErrorSummary(result, isError); message != "" {
		return true, message
	}
	return false, ""
}

func failureActionVerb(tool, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "execute", "command_execute", "command_run":
		return "Command failed"
	case "edit":
		return "Edit failed"
	case "read", "file_read", "source_read", "context_query":
		return "Read failed"
	case "session", "session_open", "session_manage", "session_read", "session_transition":
		return "Session failed"
	case "plan", "plan_create", "plan_manage", "plan_read", "plan_transition":
		return "Plan failed"
	case "artifact", "artifact_manage", "artifact_read", "artifact_register":
		return "Artifact failed"
	case "progress":
		return "Progress failed"
	}
	if value := strings.TrimSpace(tool); value != "" {
		value = strings.ReplaceAll(value, "_", " ")
		return strings.ToUpper(value[:1]) + value[1:] + " failed"
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" && fallback != "Observed" {
		return fallback + " failed"
	}
	return "Operation failed"
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
				if message := failureSummary(text); message != "" {
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
				if summary := failureSummary(message); summary != "" {
					return summary
				}
			}
			return "operation failed"
		}
	}
	return ""
}

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

func failureSummary(value string) string {
	const maxFailureLines = 3
	lines := make([]string, 0, maxFailureLines)
	for _, raw := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || isFailureHeader(line) {
			continue
		}
		lines = append(lines, compactCodeLine(line))
		if len(lines) == maxFailureLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func isFailureHeader(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "context:", "error:", "errors:", "detail:", "details:", "message:", "cause:":
		return true
	default:
		return false
	}
}

func failureDisplay(value string) string {
	summary := failureSummary(value)
	if summary == "" {
		return ""
	}
	lines := strings.Split(summary, "\n")
	lines[0] = "failed: " + lines[0]
	return strings.Join(lines, "\n")
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
	if tool == "context_query" || tool == "source_read" || tool == "file_read" {
		summary := contextQueryOutputSummary(result)
		if tool == "source_read" || tool == "file_read" {
			if format := sourceFormatSummary(result); format != "" {
				if summary == "" {
					return format
				}
				return summary + " · " + format
			}
		}
		if summary != "" {
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

func sourceFormatSummary(result map[string]any) string {
	structured, _ := result["structured_content"].(map[string]any)
	if structured == nil {
		return ""
	}
	if format, ok := structured["format"].(map[string]any); ok {
		return formatMetadataSummary(structured, format)
	}
	items, _ := structured["items"].([]any)
	if len(items) == 0 {
		return ""
	}
	formats := make([]string, 0, 2)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		format, _ := item["format"].(map[string]any)
		if summary := formatMetadataSummary(item, format); summary != "" {
			formats = append(formats, summary)
		}
		if len(formats) == 2 {
			break
		}
	}
	if len(formats) == 0 {
		return ""
	}
	if len(formats) == 1 {
		return formats[0]
	}
	return fmt.Sprintf("formats: %d files", len(items))
}

func formatMetadataSummary(item, format map[string]any) string {
	if len(format) == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	charset, _ := format["charset"].(string)
	if strings.TrimSpace(charset) != "" {
		parts = append(parts, "format="+strings.TrimSpace(charset))
	}
	bom, _ := format["bom"].(string)
	if strings.TrimSpace(bom) != "" && bom != "none" {
		parts = append(parts, "bom="+strings.TrimSpace(bom))
	}
	lineEnding, _ := format["line_ending"].(string)
	if strings.TrimSpace(lineEnding) != "" && lineEnding != "none" {
		parts = append(parts, "line-ending="+strings.TrimSpace(lineEnding))
	}
	if finalNewline, ok := format["final_newline"].(bool); ok {
		if finalNewline {
			parts = append(parts, "final-newline=yes")
		} else {
			parts = append(parts, "final-newline=no")
		}
	}
	if revision, _ := item["sha256"].(string); strings.TrimSpace(revision) != "" {
		parts = append(parts, "sha256="+strings.TrimSpace(revision))
	}
	return strings.Join(parts, " · ")
}

func structuredToolOutputSummary(tool string, data map[string]any) string {
	switch tool {
	case "extension_manage", "extension_discover":
		return extensionManageOutputSummary(data)
	case "runtime_inspect", "runtime_read":
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
	case "plan_manage", "plan_create", "plan_read", "plan_transition":
		return planManageOutputSummary(data)
	case "environment_inspect", "environment_read", "environment_snapshot_create":
		return environmentInspectOutputSummary(data)
	case "runtime_inspect", "runtime_read":
		return runtimeInspectOutputSummary(data)
	case "workspace_state", "workspace_observe":
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
	if taskID, _ := data["plan_task_id"].(string); strings.TrimSpace(taskID) != "" {
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
		id, _ := task["plan_task_id"].(string)
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
	summary = compactLine(summary)
	if len(paths) > 20 {
		summary += " (first 20 shown)"
	}
	return summary + ": " + compactPathList(paths)
}

func compactPathList(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	maxShown := 20 // reasonable limit for terminal + folding
	if len(paths) <= maxShown {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:maxShown], ", ") + fmt.Sprintf(", ... +%d more", len(paths)-maxShown)
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
		return failureSummary(details)
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
	message = failureSummary(message)
	switch {
	case code != "" && message != "":
		lines := strings.Split(message, "\n")
		lines[0] = compactCodeLine(code + ": " + lines[0])
		return strings.Join(lines, "\n")
	case message != "":
		return message
	case code != "":
		return compactCodeLine(code)
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
