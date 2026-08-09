package server

import (
	"mcpx/internal/edit"
	"mcpx/internal/file"
	"mcpx/internal/operation"
	"mcpx/internal/source"
)

const MaxReadItems = 20
const MaxMoveOutTargets = 10000
const MaxMoveOutResponsePreviewTargets = 20

// publishedLimits is the single source for hard request limits exposed to
// models through runtime capabilities. Tool schemas repeat the limits where a
// JSON Schema validator can enforce them before invocation.
func publishedLimits() map[string]any {
	return map[string]any{
		"read": map[string]any{
			"max_source_bytes":   file.MaxSourceBytes,
			"max_items":          MaxReadItems,
			"max_direct_entries": source.MaxDirectListEntries,
		},
		"operation_batch": map[string]any{
			"max_steps": operation.MaxSteps,
		},
		"edit": map[string]any{
			"max_changed_lines": edit.MaxChangedLines,
		},
		"move_out": map[string]any{
			"max_targets":                  MaxMoveOutTargets,
			"max_response_preview_targets": MaxMoveOutResponsePreviewTargets,
		},
	}
}
