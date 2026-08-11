package observation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

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
		if err := writeEventAction(w, event, "Changed", label, ansiGreen, options.colorMode); err != nil {
			return err
		}
		return writeChildren(w, []string{"file details unavailable"}, options.colorMode)
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
	if err := writeEventActionWithDiffStats(w, event, fileChangeVerb(files), label, actionColor(event.toolOrType(), false, options.colorMode), added, removed, options.colorMode); err != nil {
		return err
	}
	for index, file := range files {
		if options.diffMode != DiffModeFull && index >= maxChangedFiles {
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
					message := "diff preview truncated; use -diff full for complete output"
					if options.diffMode == DiffModeFull {
						message = "stored diff is incomplete; use observe(view=diff) to read the durable edit record"
					}
					if err := writeChild(w, message, options.colorMode); err != nil {
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
				message := "diff preview truncated; use -diff full for complete output"
				if options.diffMode == DiffModeFull {
					message = "stored diff is incomplete; use observe(view=diff) to read the durable edit record"
				}
				if err := writeChild(w, message, options.colorMode); err != nil {
					return err
				}
			}
		}
	}
	if options.diffMode != DiffModeFull && len(files) > maxChangedFiles {
		if err := writeChild(w, fmt.Sprintf("... and %d more files", len(files)-maxChangedFiles), options.colorMode); err != nil {
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
	if err := writeEventAction(w, event, verb, label, ansiGreen, options.colorMode); err != nil {
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
		if err := writeChild(w, line, options.colorMode); err != nil {
			return true, err
		}
	}
	if payload.TargetPreviewTruncated {
		if err := writeChild(w, fmt.Sprintf("... and %d more targets", total-len(payload.TargetPreview)), options.colorMode); err != nil {
			return true, err
		}
	}
	summary := fmt.Sprintf("Moved %d · failed %d", payload.MovedCount, payload.FailedCount)
	if payload.Reversible {
		summary += " · reversible"
	} else {
		summary += " · not reversible"
	}
	if err := writeChild(w, summary, options.colorMode); err != nil {
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
