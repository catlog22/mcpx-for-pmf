package server

import (
	"mcpx/internal/edit"
	"mcpx/internal/file"
	"mcpx/internal/operation"
)

const MaxReadItems = 20
const MaxRemoveTargets = 10000
const MaxRemoveManifestEntries = 100000

// publishedLimits is the single source for hard request limits exposed to
// models through runtime capabilities. Tool schemas repeat the limits where a
// JSON Schema validator can enforce them before invocation.
func publishedLimits() map[string]any {
	return map[string]any{
		"read": map[string]any{
			"max_source_bytes": file.MaxSourceBytes,
			"max_items":        MaxReadItems,
		},
		"operation_batch": map[string]any{
			"max_steps": operation.MaxSteps,
		},
		"edit": map[string]any{
			"max_changed_lines": edit.MaxChangedLines,
		},
		"remove_prepare": map[string]any{
			"max_targets":          MaxRemoveTargets,
			"max_manifest_entries": MaxRemoveManifestEntries,
		},
		"submit_remove": map[string]any{
			"max_manifest_entries": MaxRemoveManifestEntries,
		},
	}
}
