package server

import (
	"fmt"
	"strings"
	"sync"

	"mcpx/internal/envelope"
	"mcpx/internal/server/guidance"
)

const agentGuidanceVersion = "1.19"

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
		"edit_payload": map[string]any{
			"tool":      config.EditPayload.Tool,
			"required":  config.EditPayload.Required,
			"retry":     config.EditPayload.Retry,
			"edit_item": config.EditPayload.EditItem,
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
		"每个意图都使用对应的 MCPX 工具；不要用 execute 替代文件或 Git 读取。",
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
	lines = append(lines, "", "edit 参数速查（工具 schema 不可用时使用）：")
	lines = append(lines, "- 必填："+strings.Join(config.EditPayload.Required, "、"))
	lines = append(lines, "- 重试："+config.EditPayload.Retry)
	lines = append(lines, "- edit 项："+fmt.Sprint(config.EditPayload.EditItem["operation"]))
	for _, key := range []string{"create", "update", "rename", "delete", "replacement", "limit"} {
		if value, ok := config.EditPayload.EditItem[key].(string); ok {
			lines = append(lines, "  - "+key+"："+value)
		}
	}
	lines = append(lines, "- 其他模式：超出单批次上限时拆分为多个 edit 调用")
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
	case "workspace_read":
		tool = "read"
		setView("list")
	case "source_read", "file_read", "context_query":
		legacyTool := tool
		tool = "read"
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
		tool = "observe"
		if action == "history" {
			setView("history")
		} else {
			setView("changes")
		}
	case "change_execute":
		tool = "edit"
		delete(result, "action")
	case "change", "change_read":
		legacyTool := tool
		tool = "observe"
		if action == "history" || legacyTool == "change_read" {
			setView("history")
		} else {
			setView("changes")
		}
	case "command_execute":
		tool = "execute"
		if action == "" {
			result["action"] = "run"
		}
	case "task_manage":
		if action == "attach" || action == "stop" || action == "stdin" {
			tool = "execute"
			result["action"] = action
			delete(result, "operation")
		} else {
			tool = "observe"
			setView(action)
		}
	case "plan_manage":
		switch action {
		case "create":
			tool = "plan"
			result["action"] = "create"
		case "get":
			tool = "plan"
			result["action"] = "read"
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
		tool = "observe"
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
			tool = "discover"
			setView(action)
		}
	case "artifact_manage":
		tool = "artifact"
		result["action"] = action
	case "secrets_provide":
		tool = "secret_provide"
	}
	if isCleanPublicTool(tool) {
		if sessionID, ok := result["session_id"]; ok {
			if _, exists := result["remote_session_id"]; !exists {
				result["remote_session_id"] = sessionID
			}
			delete(result, "session_id")
		}
	} else if sessionID, ok := result["remote_session_id"]; ok {
		result["session_id"] = sessionID
		delete(result, "remote_session_id")
	}
	return tool, result
}

func isCleanPublicTool(tool string) bool {
	switch tool {
	case "session", "read", "edit", "observe", "execute", "plan", "artifact", "discover", "skill_call", "mcp_call",
		"operation_batch", "operation_manage", "runtime_read", "environment_read", "environment", "screenshot_capture", "secret_provide":
		return true
	default:
		return false
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
