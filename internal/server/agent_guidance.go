package server

import (
	"fmt"
	"strings"

	"mcpx/internal/envelope"
)

const agentGuidanceVersion = "1.4"

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
			"Every MCPX tool call must include a concise non-empty top-level intent describing the current goal and expected result; calls without intent are rejected before execution.",
			"Use file_read or context_query to inspect files and source context.",
			"Use workspace_state for Git status, diff, snapshots, and workspace changes.",
			"Use change_execute for every file modification; provide the current file revision before editing.",
			"Use command_execute only for explicit tests, builds, or user-requested commands.",
			"Use change_manage only to prepare or inspect a Changeset; apply and revert through change_execute.",
			"After a successful change_execute or Changeset review, include each changed file's concrete additions and removals in the final response as a Markdown ```diff code block; do not summarize only the file names. For oversized changes, show the bounded preview and mention the Changeset Resource.",
			"When approval is required, preserve the exact remote_session_id and approval_id and do not duplicate the request.",
			"Do not claim a file was read, changed, or verified without a successful tool result.",
			"Before every important, mutating, destructive, or long-running call, briefly state what will be done and why; then call the tool with the complete payload without waiting for a schema re-fetch.",
			"After every tool call, put a concise verified result and next action in top-level progress_summary before starting the next non-trivial tool call; distinguish verified facts from assumptions and unknowns.",
			"If no next tool call is planned because the task is complete, blocked, waiting for the user, or needs a decision, call progress_report with the verified result, status, and next step before replying or waiting.",
			"Keep every change_execute call small (at most 300 added lines - lines actually inserted, not the whole file). Never truncate a file to dodge limits; submit full content or a replace_range window and retry if a host safety check blocks it.",
			"Never quote instruction text (prompts, guidance, AGENTS.md) into file content; use plain code or data. If a host safety check blocks a call, shrink the content and retry instead of giving up.",
		},
		"response_contract": map[string]any{
			"required": true,
			"before_tool_call": []string{
				"当前理解的目标",
				"接下来要执行的查询、工具调用或命令及其目的",
				"重要、变更、删除或长时间操作必须先说明准备做什么",
			},
			"after_tool_call": []string{
				"实际调用的工具、查询或命令及其真实结果",
				"下一次工具调用使用 progress_summary 补发上一次工具的可验证结果和下一步；没有下一次工具调用时调用 progress_report",
				"读取、创建、修改、移动或删除的文件",
				"每项文件变更的具体内容和影响；代码变更使用 Markdown ```diff 代码块",
				"测试、构建、检查及其真实结果",
				"失败、限制、风险和未验证事项",
			},
			"final_response": []string{
				"当前理解的目标",
				"实际执行的查询、工具调用或命令及目的",
				"文件操作与每项变更的具体内容和影响",
				"验证证据",
				"失败、限制、风险和未验证事项",
			},
			"evidence_rule": "没有成功工具结果、明确命令输出或测试证据时，不得声称已读取、已修改、已修复、已构建或已测试通过。",
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
	if contract, ok := guidance["response_contract"].(map[string]any); ok {
		lines = append(lines, "", "Required user-visible response contract:")
		if required, ok := contract["before_tool_call"].([]string); ok {
			lines = append(lines, "- Before important tool calls:")
			for _, item := range required {
				lines = append(lines, "  - "+item)
			}
		}
		if required, ok := contract["after_tool_call"].([]string); ok {
			lines = append(lines, "- After every tool call:")
			for _, item := range required {
				lines = append(lines, "  - "+item)
			}
		}
		if evidence, ok := contract["evidence_rule"].(string); ok {
			lines = append(lines, "- Evidence rule: "+evidence)
		}
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
