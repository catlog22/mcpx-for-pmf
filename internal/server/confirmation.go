package server

import "mcpx/internal/approval"

// pendingConfirmationItems exposes resumable semantic-confirmation context
// without exposing the internal store ID or a separate management action.
func pendingConfirmationItems(pending []approval.Pending) []map[string]any {
	items := make([]map[string]any, 0, len(pending))
	for _, item := range pending {
		view := map[string]any{
			"tool":       item.Tool,
			"summary":    item.Summary,
			"workspace":  item.Workspace,
			"created_at": item.CreatedAt,
		}
		if item.ChangesetID != "" {
			view["changeset_id"] = item.ChangesetID
			view["expected_digest"] = item.ChangesetDigest
		}
		if item.Command != "" {
			view["command"] = item.Command
			view["purpose"] = item.Purpose
			view["scope"] = item.Scope
		}
		items = append(items, view)
	}
	return items
}
