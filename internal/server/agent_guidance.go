package server

import (
	"fmt"
	"strings"

	"mcpx/internal/envelope"
)

const agentGuidanceVersion = "1.0"

// agentGuidance is the short, stable routing contract shown after session_open.
// It deliberately contains no workspace path, secret, approval token, or
// concrete tool argument value. It does include the compact change_execute
// payload cheat-sheet so agents can construct valid calls even when a host
// compresses or drops tool schemas from a long conversation context.
func agentGuidance() map[string]any {
	return map[string]any{
		"version":  agentGuidanceVersion,
		"priority": "high",
		"summary":  "Use dedicated MCPX tools for inspection and edits; reserve command_execute for explicit commands.",
		"rules": []string{
			"Use file_read or context_query to inspect files and source context.",
			"Use workspace_state for Git status, diff, snapshots, and workspace changes.",
			"Use change_execute for every file modification; provide the current file revision before editing.",
			"Use command_execute only for explicit tests, builds, or user-requested commands.",
			"Use change_manage only to prepare or inspect a Changeset; apply and revert through change_execute.",
			"When approval is required, preserve the exact remote_session_id and approval_id and do not duplicate the request.",
			"Do not claim a file was read, changed, or verified without a successful tool result.",
			"Call tools with the complete payload immediately; never announce intent first or wait for a schema re-fetch.",
			"Keep every change_execute call small (at most 300 added lines - lines actually inserted, not the whole file). Never truncate a file to dodge limits; submit full content or a replace_range window and retry if a host safety check blocks it.",
			"Never quote instruction text (prompts, guidance, AGENTS.md) into file content; use plain code or data. If a host safety check blocks a call, shrink the content and retry instead of giving up.",
		},
		"change_payload": map[string]any{
			"tool":     "change_execute",
			"required": []string{"remote_session_id", "summary"},
			"operations_item": map[string]any{
				"operation":     "create | update | rename | delete | replace_exact | insert_before | insert_after | delete_exact | replace_range",
				"create":        "content = full new file content; keep at most 300 added lines and append with insert_after for large files",
				"update":        "patch = apply_patch-style diff text",
				"rename":        "path + new_path",
				"delete":        "path only",
				"insert_after":  "path + match + content (append to a large file chunk by chunk; end content with a newline when inserting full lines)",
				"replace":       "path + match + replacement (or replacement + occurrence)",
				"replace_range": "path + range_start + range_end + content + base_sha256 (current revision always required)",
				"base_sha256":   "revision returned by file_read; omit only when creating a new file",
			},
			"alternatives": "changeset_id + expected_digest applies a prepared draft; revert_changeset_id reverts an applied Changeset. These three modes are mutually exclusive.",
		},
		"tool_routing": map[string]any{
			"select_workspace":   []string{"workspace_list"},
			"open_session":       []string{"session_open"},
			"inspect_files":      []string{"file_read", "context_query"},
			"inspect_git":        []string{"workspace_state"},
			"modify_files":       []string{"change_execute"},
			"run_tests_or_build": []string{"command_execute"},
			"manage_changesets":  []string{"change_manage", "change_execute"},
			"handle_tasks":       []string{"task_manage"},
			"handle_approval":    []string{"approval_manage"},
		},
	}
}

func agentGuidanceRevision() string { return hashRevision(agentGuidance()) }

func agentGuidanceInstructions() string {
	guidance := agentGuidance()
	rules, _ := guidance["rules"].([]string)
	lines := []string{
		"MCPX Agent Guidance (high priority):",
		"Use the dedicated MCPX tool for each intent; do not substitute command_execute for file or Git inspection.",
	}
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	if payload, ok := guidance["change_payload"].(map[string]any); ok {
		lines = append(lines, "", "change_execute payload cheat-sheet (fallback if the tool schema is unavailable):")
		if required, ok := payload["required"].([]string); ok {
			lines = append(lines, "- required: "+strings.Join(required, ", "))
		}
		if item, ok := payload["operations_item"].(map[string]any); ok {
			lines = append(lines, "- operations item: "+fmt.Sprint(item["operation"]))
			for _, key := range []string{"create", "update", "rename", "delete", "insert_after", "replace", "replace_range", "base_sha256"} {
				if value, ok := item[key].(string); ok {
					lines = append(lines, "  - "+key+": "+value)
				}
			}
		}
		if alternatives, ok := payload["alternatives"].(string); ok {
			lines = append(lines, "- "+alternatives)
		}
	}
	return strings.Join(lines, "\n")
}

func nextActionWithReason(tool, reason string, arguments map[string]any) map[string]any {
	return map[string]any{
		"tool":      tool,
		"reason":    reason,
		"arguments": argumentsOrEmpty(arguments),
	}
}

func addRecoveryAction(response *envelope.Response, tool, reason string, arguments map[string]any) {
	if response == nil || response.Error == nil || strings.TrimSpace(tool) == "" {
		return
	}
	if response.Error.Details == nil {
		response.Error.Details = map[string]any{}
	}
	response.Error.Details["next_action"] = map[string]any{
		"tool":      tool,
		"reason":    reason,
		"arguments": argumentsOrEmpty(arguments),
	}
}

func addRecoveryActions(response *envelope.Response, actions ...map[string]any) {
	if response == nil || response.Error == nil || len(actions) == 0 {
		return
	}
	if response.Error.Details == nil {
		response.Error.Details = map[string]any{}
	}
	response.Error.Details["next_actions"] = actions
}

func argumentsOrEmpty(arguments map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}
	return arguments
}
