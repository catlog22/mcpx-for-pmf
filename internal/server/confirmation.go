package server

import "mcpx/internal/approval"

func cleanPendingConfirmationItems(pending []map[string]any) []map[string]any {
	items := make([]map[string]any, 0, len(pending))
	for _, original := range pending {
		item := map[string]any{}
		for _, key := range []string{"summary", "workspace", "created_at", "command", "purpose", "scope"} {
			if value, ok := original[key]; ok {
				item[key] = value
			}
		}
		item["tool"] = "execute"
		item["user_confirmed_required"] = true
		items = append(items, item)
	}
	return items
}

// pendingConfirmationItems exposes resumable semantic-confirmation context
// without exposing the internal store ID or a separate management action.
func pendingConfirmationItems(pending []approval.Pending) []map[string]any {
	items := make([]map[string]any, 0, len(pending))
	for _, item := range pending {
		view := map[string]any{
			"tool":               item.Tool,
			"summary":            item.Summary,
			"workspace":          item.Workspace,
			"created_at":         item.CreatedAt,
			"confirmation_token": item.ConfirmationToken,
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
