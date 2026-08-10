package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

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
		if err := writeCommandAction(w, event, "Ran", compactCommand(command), actionColor(event.Tool, false, options.colorMode), options.colorMode); err != nil {
			return err
		}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if !isREPLPrompt(line) {
			filtered = append(filtered, line)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if !options.suppressOutputAction {
		if command != "" {
			if err := writeCommandStreamHeader(w, stream, options.colorMode); err != nil {
				return err
			}
		} else {
			if err := writeEventAction(w, event, "Read", stream, commandStreamColor(event.Stream), options.colorMode); err != nil {
				return err
			}
		}
	}
	for i, line := range filtered {
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

// isREPLPrompt reports whether a command output line is a bare interactive
// interpreter prompt (python's ">>>", possibly repeated or spaced). Prompt
// lines add noise to terminal observation without carrying command output;
// lines that contain any other content (">>> x") are kept.
func isREPLPrompt(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	for _, current := range trimmed {
		if current != '>' && current != ' ' && current != '\t' {
			return false
		}
	}
	return strings.Contains(trimmed, ">>>")
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
