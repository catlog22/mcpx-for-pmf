package server

import (
	"fmt"
	"strings"
	"sync"

	"mcpx/internal/envelope"
	"mcpx/internal/server/guidance"
)

const agentGuidanceVersion = "1.18"

// agentGuidanceConfig mirrors guidance.Config for existing call sites.
type agentGuidanceConfig = guidance.Config

var (
	defaultAgentGuidance     agentGuidanceConfig
	defaultAgentGuidanceOnce sync.Once
)

func loadDefaultAgentGuidance() agentGuidanceConfig {
	defaultAgentGuidanceOnce.Do(func() {
		defaultAgentGuidance = guidance.MustLoadAgent()
		if defaultAgentGuidance.Version != agentGuidanceVersion {
			// Keep code constant and YAML in lockstep during local edits.
			defaultAgentGuidance.Version = agentGuidanceVersion
		}
	})
	return defaultAgentGuidance
}

// agentGuidance is the short, stable routing contract shown after session（action=open）.
// It deliberately contains no workspace path, secret, confirmation token, or
// concrete tool argument value. It does include the compact change
// payload cheat-sheet so agents can construct valid calls even when a host
// compresses or drops tool schemas from a long conversation context.
func agentGuidance() map[string]any {
	config := loadDefaultAgentGuidance()
	return map[string]any{
		"version":  config.Version,
		"priority": config.Priority,
		"summary":  config.Summary,
		"rules":    config.Rules,
		"response_contract": map[string]any{
			"required":         config.ResponseContract.Required,
			"before_tool_call": config.ResponseContract.BeforeToolCall,
			"after_tool_call":  config.ResponseContract.AfterToolCall,
			"final_response":   config.ResponseContract.FinalResponse,
			"evidence_rule":    config.ResponseContract.EvidenceRule,
		},
		"change_payload": map[string]any{
			"tool":            config.ChangePayload.Tool,
			"required":        config.ChangePayload.Required,
			"confirmation":    config.ChangePayload.Confirmation,
			"operations_item": config.ChangePayload.OperationsItem,
			"alternatives":    config.ChangePayload.Alternatives,
		},
		"tool_routing": config.ToolRouting,
	}
}

func agentGuidanceRevision() string { return hashRevision(agentGuidance()) }

func agentGuidanceInstructions() string {
	config := loadDefaultAgentGuidance()
	rules := config.Rules
	lines := []string{
		"MCPX Agent 指引（高优先级）：",
		"每个意图都使用对应的 MCPX 工具；不要用 command_run 替代文件或 Git 检查。",
	}
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	lines = append(lines, "", "用户可见响应契约：")
	lines = append(lines, "- 重要工具调用前：")
	for _, item := range config.ResponseContract.BeforeToolCall {
		lines = append(lines, "  - "+item)
	}
	lines = append(lines, "- 每次工具调用后：")
	for _, item := range config.ResponseContract.AfterToolCall {
		lines = append(lines, "  - "+item)
	}
	lines = append(lines, "- 证据规则："+config.ResponseContract.EvidenceRule)
	lines = append(lines, "", "change 参数速查（工具 schema 不可用时使用）：")
	lines = append(lines, "- 必填："+strings.Join(config.ChangePayload.Required, "、"))
	lines = append(lines, "- confirmation_token："+config.ChangePayload.Confirmation)
	lines = append(lines, "- operations 项："+fmt.Sprint(config.ChangePayload.OperationsItem["operation"]))
	for _, key := range []string{"create", "update", "rename", "delete", "insert_after", "replace", "replace_range", "base_sha256"} {
		if value, ok := config.ChangePayload.OperationsItem[key].(string); ok {
			lines = append(lines, "  - "+key+"："+value)
		}
	}
	lines = append(lines, "- 其他模式："+config.ChangePayload.Alternatives)
	return strings.Join(lines, "\n")
}

func nextActionWithReason(tool, reason string, arguments map[string]any) map[string]any {
	tool, arguments = normalizePublicAction(tool, arguments)
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
	tool, arguments = normalizePublicAction(tool, arguments)
	response.Error.Recovery = &envelope.Recovery{Action: tool, Tool: tool, Arguments: argumentsOrEmpty(arguments)}
	response.Error.Details["next_action"] = map[string]any{
		"tool":      tool,
		"reason":    reason,
		"arguments": argumentsOrEmpty(arguments),
	}
}

func normalizePublicAction(tool string, arguments map[string]any) (string, map[string]any) {
	result := argumentsOrEmpty(arguments)
	action, _ := result["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	setView := func(view string) {
		delete(result, "action")
		result["view"] = view
	}
	switch tool {
	case "file_read", "context_query":
		legacyTool := tool
		tool = "source_read"
		if legacyTool == "file_read" || action == "" {
			setView("file")
		} else if action == "list" {
			setView("list")
		} else if action == "query" {
			setView("context")
		} else {
			setView("search")
		}
	case "change_manage":
		if action == "prepare" || action == "discard" {
			tool = "change"
			result["action"] = action
		} else {
			tool = "change_read"
			setView(action)
		}
	case "change_execute":
		if _, ok := result["revert_changeset_id"]; ok {
			tool = "change"
			if value, ok := result["revert_changeset_id"]; ok {
				result["changeset_id"] = value
			}
			delete(result, "revert_changeset_id")
			result["action"] = "revert"
		} else if _, ok := result["changeset_id"]; ok {
			tool = "change"
			result["action"] = "apply"
		} else {
			tool = "change"
			result["action"] = "prepare"
		}
	case "command_execute":
		tool = "command_run"
	case "task_manage":
		if action == "attach" || action == "stop" || action == "stdin" {
			tool = "task"
			result["action"] = action
			delete(result, "operation")
		} else {
			tool = "task_read"
			setView(action)
		}
	case "plan_manage":
		switch action {
		case "create":
			tool = "plan"
			result["action"] = "create"
		case "get":
			tool = "plan_read"
			delete(result, "action")
		default:
			tool = "plan"
			result["action"] = action
			delete(result, "transition")
		}
	case "runtime_inspect":
		tool = "runtime_read"
		setView(action)
	case "environment_inspect":
		if value, ok := result["save_snapshot"].(bool); ok && value {
			tool = "environment"
			result["action"] = "snapshot_create"
			delete(result, "save_snapshot")
		} else {
			tool = "environment_read"
			if _, ok := result["compare_to"]; ok {
				result["snapshot_id"] = result["compare_to"]
				delete(result, "compare_to")
			}
			if action == "" {
				setView("current")
			} else {
				setView(action)
			}
		}
	case "workspace_state":
		tool = "workspace_read"
		setView(action)
	case "extension_manage":
		kind, _ := result["kind"].(string)
		if action == "call" && strings.EqualFold(kind, "skill") {
			tool = "skill_call"
			delete(result, "action")
			delete(result, "kind")
		} else if action == "call" && strings.EqualFold(kind, "mcp") {
			tool = "mcp_call"
			delete(result, "action")
			delete(result, "kind")
		} else {
			tool = "extension_discover"
			delete(result, "kind")
			setView(action)
		}
	case "artifact_manage":
		if action == "register" {
			tool = "artifact"
			delete(result, "action")
		} else {
			tool = "artifact_read"
			if action == "read" {
				setView("content")
			} else {
				setView("list")
			}
		}
	case "secrets_provide":
		tool = "secret_provide"
	}
	if sessionID, ok := result["remote_session_id"]; ok {
		result["session_id"] = sessionID
		delete(result, "remote_session_id")
	}
	return tool, result
}

func addRecoveryActions(response *envelope.Response, actions ...map[string]any) {
	if response == nil || response.Error == nil || len(actions) == 0 {
		return
	}
	if response.Error.Details == nil {
		response.Error.Details = map[string]any{}
	}
	response.Error.Details["next_actions"] = actions
	if len(actions) > 0 {
		if tool, _ := actions[0]["tool"].(string); tool != "" {
			arguments, _ := actions[0]["arguments"].(map[string]any)
			response.Error.Recovery = &envelope.Recovery{Action: tool, Tool: tool, Arguments: argumentsOrEmpty(arguments)}
		}
	}
}

func argumentsOrEmpty(arguments map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}
	return arguments
}
