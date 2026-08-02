package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiBlue   = "\033[36m"
)

// RenderText writes a human-oriented timeline item. File change events use a
// Markdown diff fence so terminals and copied conversation summaries retain
// the same +/- semantics as a code review view.
func RenderText(w io.Writer, event Event, color bool) error {
	if w == nil {
		return fmt.Errorf("render writer is required")
	}
	header := eventHeader(event)
	switch event.Type {
	case TypeToolStarted:
		if _, err := fmt.Fprintf(w, "%s%s %s\n", header, paint("TOOL STARTED", ansiBlue, color), event.Tool); err != nil {
			return err
		}
		if event.Intent != "" {
			if _, err := fmt.Fprintf(w, "  INTENT: %s\n", event.Intent); err != nil {
				return err
			}
		}
		return writeJSONBlock(w, "  INPUT", event.Input)
	case TypeToolCompleted:
		return renderToolCompleted(w, header, event, color)
	case TypeCommandOutput:
		return renderCommandOutput(w, header, event, color)
	case TypeFileChanged:
		return renderFileChanged(w, header, event, color)
	case TypeSessionLifecycle:
		if _, err := fmt.Fprintf(w, "%s%s %s\n", header, paint("SESSION", ansiBlue, color), event.Summary); err != nil {
			return err
		}
		return writeJSONBlock(w, "  DETAILS", event.Output)
	case TypeObserverNotice:
		if _, err := fmt.Fprintf(w, "%s%s %s\n", header, paint("NOTICE", ansiYellow, color), event.Summary); err != nil {
			return err
		}
		return writeJSONBlock(w, "  DETAILS", event.Output)
	default:
		if _, err := fmt.Fprintf(w, "%s%s %s\n", header, paint("EVENT", ansiBlue, color), event.Type); err != nil {
			return err
		}
		return writeJSONBlock(w, "  DATA", event.Output)
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

func renderToolCompleted(w io.Writer, header string, event Event, color bool) error {
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	status, _ := payload["status"].(string)
	if status == "" {
		status = "unknown"
	}
	statusColor := ansiGreen
	if status == "error" {
		statusColor = ansiRed
	}
	timing, _ := payload["timing"].(map[string]any)
	elapsed := formatNumber(timing["server_elapsed_ms"])
	line := fmt.Sprintf("%s%s %s status=%s", header, paint("TOOL COMPLETED", ansiBlue, color), event.Tool, paint(status, statusColor, color))
	if elapsed != "" {
		line += " elapsed=" + elapsed + "ms"
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	if event.Intent != "" {
		if _, err := fmt.Fprintf(w, "  INTENT: %s\n", event.Intent); err != nil {
			return err
		}
	}
	if message, ok := payload["error"].(string); ok && message != "" {
		if _, err := fmt.Fprintf(w, "  ERROR: %s\n", paint(message, ansiRed, color)); err != nil {
			return err
		}
	}
	if result, ok := payload["result"]; ok {
		encoded, _ := json.Marshal(result)
		if err := writeJSONBlock(w, "  OUTPUT", encoded); err != nil {
			return err
		}
	}
	if event.Truncated {
		_, _ = fmt.Fprintln(w, paint("  OUTPUT TRUNCATED; see the linked resource or task/change history.", ansiYellow, color))
	}
	return nil
}

func renderCommandOutput(w io.Writer, header string, event Event, color bool) error {
	var payload map[string]any
	_ = json.Unmarshal(event.Output, &payload)
	text, _ := payload["text"].(string)
	if _, err := fmt.Fprintf(w, "%s%s stream=%s offset=%d\n", header, paint("COMMAND OUTPUT", ansiBlue, color), event.Stream, event.Offset); err != nil {
		return err
	}
	if text != "" {
		if _, err := fmt.Fprintln(w, text); err != nil {
			return err
		}
	}
	if event.Truncated {
		_, _ = fmt.Fprintln(w, paint("  OUTPUT TRUNCATED", ansiYellow, color))
	}
	return nil
}

func renderFileChanged(w io.Writer, header string, event Event, color bool) error {
	if _, err := fmt.Fprintf(w, "%s%s %s\n", header, paint("FILE CHANGES", ansiGreen, color), event.Summary); err != nil {
		return err
	}
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
		return writeJSONBlock(w, "  FILE DATA", event.Output)
	}
	for _, file := range payload.Files {
		path := file.Path
		if file.NewPath != "" && file.NewPath != file.Path {
			path += " -> " + file.NewPath
		}
		if _, err := fmt.Fprintf(w, "  %s %s\n", paint(file.Operation, ansiGreen, color), path); err != nil {
			return err
		}
		if file.Diff != "" {
			if _, err := fmt.Fprintf(w, "```diff\n%s\n```\n", strings.TrimSuffix(file.Diff, "\n")); err != nil {
				return err
			}
		}
		if file.DiffTruncated {
			if _, err := fmt.Fprintln(w, paint("  FILE DIFF TRUNCATED", ansiYellow, color)); err != nil {
				return err
			}
		}
	}
	if event.ResourceURI != "" {
		_, _ = fmt.Fprintf(w, "  RESOURCE: %s\n", event.ResourceURI)
	} else if payload.Diff.ResourceURI != "" {
		_, _ = fmt.Fprintf(w, "  RESOURCE: %s\n", payload.Diff.ResourceURI)
	}
	if event.Truncated {
		_, _ = fmt.Fprintln(w, paint("  FILE CHANGE DATA TRUNCATED", ansiYellow, color))
	}
	return nil
}

func eventHeader(event Event) string {
	if event.CreatedAt.IsZero() {
		return ""
	}
	return "[" + event.CreatedAt.Local().Format("15:04:05") + "] "
}

func writeJSONBlock(w io.Writer, label string, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		_, err = fmt.Fprintf(w, "%s: %s\n", label, string(raw))
		return err
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s:\n```json\n%s\n```\n", label, pretty)
	return err
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
