package server

import "mcpx/internal/approval"

// pendingConfirmationItems builds the session bootstrap view for commands that
// still need an explicit user confirmation.
func pendingConfirmationItems(pending []approval.Pending) []map[string]any {
	items := make([]map[string]any, 0, len(pending))
	for _, pendingItem := range pending {
		item := map[string]any{
			"tool":                    "execute",
			"summary":                 pendingItem.Summary,
			"workspace":               pendingItem.Workspace,
			"created_at":              pendingItem.CreatedAt,
			"user_confirmed_required": true,
		}
		if pendingItem.Command != "" {
			item["command"] = pendingItem.Command
			item["purpose"] = pendingItem.Purpose
			item["scope"] = pendingItem.Scope
		}
		items = append(items, item)
	}
	return items
}
