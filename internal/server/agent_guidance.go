package server

import (
	"fmt"
	"strings"

	"mcpx/internal/envelope"
)

const agentGuidanceVersion = "1.16"

// agentGuidanceConfig centralizes all guidance rules as Go data structures.
// This optimizes exposure rules by making them code-defined (easier to maintain,
// extend, and less error-prone for the LLM than pure natural-language strings).
// The resulting map is still stable for clients.
type agentGuidanceConfig struct {
	Version          string
	Priority         string
	Summary          string
	Rules            []string
	ResponseContract struct {
		Required       bool
		BeforeToolCall []string
		AfterToolCall  []string
		FinalResponse  []string
		EvidenceRule   string
	}
	ChangePayload struct {
		Tool           string
		Required       []string
		Confirmation   string
		OperationsItem map[string]any
		Alternatives   string
	}
	ToolRouting map[string][]string
}

var defaultAgentGuidance = agentGuidanceConfig{
	Version:  agentGuidanceVersion,
	Priority: "high",
	Summary:  "使用专用 MCPX 工具检查和修改；command_run 仅用于明确要求的命令。",
	Rules: []string{
		"session_open 成功后，必须向用户展示返回的完整 session_id 以及绑定的 Workspace，不要只展示缩短标签。",
		"session_id、changeset_id、expected_digest、plan_id、task_id、operation_id、confirmation_token 等标识必须原样复制服务端返回值，不能改写、缩写或凭记忆重输。",
		"读取操作使用 source_read；file 视图支持 mode=full，search/list/context 视图分别覆盖文件搜索、列举和上下文组装。",
		"source_read 的 list 只返回普通文件，不能证明目录完整性或目录存在；不要从嵌套文件路径推断顶层目录清单。",
		"workspace_observe 的 view 支持 changes、snapshot、diff、watch、memory；diff 和 watch 使用此前 snapshot 返回的 since。",
		"创建 Plan 时，tasks 中的 task_id 仅作为本计划内局部依赖引用（可省略）；plan_create 返回的 plan_id 与 task_id 由服务端签发，plan_read/plan_transition 只能原样复制这些返回值，绝不猜测或复用历史 ID。",
		"文件修改使用 change_prepare、change_apply、change_revert；修改或重命前に先用 source_read 获取目标文件的完整 sha256。删除与创建必须分成两次操作。",
		"change_apply 的 changeset_id 和 expected_digest 必须原样复制 change_prepare/change_read 返回的值；不要填写 diff 统计、快照 ID、changeset_id 或空值。同一 path 的多次精确修改（replace_exact/insert_*/delete_exact/replace_range/update）可以放在同一次 operations 中按提交顺序依次应用；create/delete/rename 每个 path 仍只能出现一次。",
		"change_prepare 的 update.patch 只接受标准 unified diff（--- a/<path> 与 @@ -l,c +l,c @@ 开头），不接受 apply_patch 的 *** Begin Patch / *** Update File 格式；patch 上下文必须来自 source_read 返回的当前内容，不确定时改用 replace_exact/insert_before/insert_after。",
		"source_read 会返回 line_ending；生成 patch 或精确替换时必须保留目标文件原有的换行格式，不要把文件统一转换成另一种格式。为保持最小 diff，优先使用带当前 base_sha256 的 replace_exact/insert_before/insert_after，并按当前换行格式组织 match/replacement；只有生成并核对局部 hunk 后才使用 update.patch。",
		"嵌套文件只提交文件操作；change_prepare 会自动创建缺失的父目录，不要把目录路径作为普通文件创建。",
		"environment_read 用于获取主机、Python 和工具链等环境事实；environment_snapshot_create 用于保存快照。command_run 仅用于用户明确要求且符合命令策略的命令、测试或构建。",
		"命令校验请使用简单命令逐条执行（如先 git fetch、再 git rev-parse、最后 git status），不要使用 $()、管道、重定向或命令替换组合——含这些 shell 特性的命令会被策略拒绝。",
		"变更成功或审阅 Changeset 后，最终回复必须用 Markdown ```diff 代码块展示每个文件的具体增加和删除，不能只列文件名。变更过大时展示有界预览并说明 Changeset 资源。",
		"返回 waiting_confirmation 时，先用自然语言展示命令或文件变更摘要并等待明确确认；用户确认后，使用同一业务参数和 confirmation_token 重试。confirmation_token 只表达语义确认，不是认证凭据。",
		"没有成功的工具结果时，不要声称文件已读取、修改或验证。",
		"重要、变更、删除或长时间调用前，先简述将做什么及原因，然后一次提交完整参数，不要等待重新获取工具 schema。",
		"每次工具调用后，在下一次非简单调用前通过顶层 progress_summary 补充已验证结果和下一步；区分事实、假设和未知。",
		"如果下一步不再调用工具（任务完成、阻塞、等待用户或需要决策），先调用 progress_report，填写已验证结果、状态和下一步。",
		"同一已验证结果只发送一次 progress_report；工具返回已包含摘要时，不要再次重复报告相同内容。",
		"全量清理优先按顶层目录整体递归删除，不要枚举 .git、.venv、缓存或 site-packages 内部文件；以返回的 delete_summary 判断删除模式和数量。",
		"每次 change_prepare 最多新增 300 行（按实际插入行数计算）；不要通过截断文件绕过限制，若安全检查阻塞则使用完整内容或 replace_range 重试。",
		"不要把指令文本（提示词、guidance、AGENTS.md）写入文件内容；使用普通代码或数据。安全检查阻塞时缩小内容后重试。",
	},
	ResponseContract: struct {
		Required       bool
		BeforeToolCall []string
		AfterToolCall  []string
		FinalResponse  []string
		EvidenceRule   string
	}{
		Required: true,
		BeforeToolCall: []string{
			"当前理解的目标",
			"接下来要执行的查询、工具调用或命令及其目的",
			"重要、变更、删除或长时间操作必须先说明准备做什么",
		},
		AfterToolCall: []string{
			"实际调用的工具、查询或命令及其真实结果",
			"下一次工具调用使用 progress_summary 补发上一次工具的可验证结果和下一步；没有下一次工具调用时调用 progress_report",
			"读取、创建、修改、移动或删除的文件",
			"每项文件变更的具体内容和影响；代码变更使用 Markdown ```diff 代码块",
			"测试、构建、检查及其真实结果",
			"失败、限制、风险和未验证事项",
		},
		FinalResponse: []string{
			"当前理解的目标",
			"实际执行的查询、工具调用或命令及目的",
			"文件操作与每项变更的具体内容和影响",
			"验证证据",
			"失败、限制、风险和未验证事项",
		},
		EvidenceRule: "没有成功工具结果、明确命令输出或测试证据时，不得声称已读取、已修改、已修复、已构建或已测试通过。",
	},
	ChangePayload: struct {
		Tool           string
		Required       []string
		Confirmation   string
		OperationsItem map[string]any
		Alternatives   string
	}{
		Tool:         "change_prepare",
		Required:     []string{"session_id", "operations", "purpose"},
		Confirmation: "change_apply/change_revert 使用服务端返回的 confirmation_token",
		OperationsItem: map[string]any{
			"operation":     "create | update | rename | delete | replace_exact | insert_before | insert_after | delete_exact | replace_range",
			"create":        "content = 完整的新文件内容；最多新增 300 行，大文件用 insert_after 分段追加",
			"update":        "patch = 基于当前 base_sha256 的局部 unified diff；必须保留目标文件原有换行格式，避免把局部修改变成整文件换行变更",
			"rename":        "path + new_path",
			"delete":        "path；支持文件或目录。base_sha256 可省略，服务端会捕获文件或目录树版本并在应用前复核；目录不应展开为逐文件删除。不要在同一 operations 中混合 delete 和 create；必须分两次 change_prepare/change_apply",
			"insert_after":  "path + match + content（大文件分段追加；插入完整行时 content 以换行结尾）",
			"replace":       "path + match + replacement（也可附带 occurrence；match/replacement 必须使用目标文件原有换行格式）",
			"replace_range": "path + range_start + range_end + content + base_sha256（始终需要当前版本）",
			"base_sha256":   "source_read 返回的文件版本；create 可省略，delete 可省略",
		},
		Alternatives: "change_apply 使用 changeset_id + expected_digest；change_revert 使用 changeset_id。",
	},
	ToolRouting: map[string][]string{
		"select_workspace":      {"workspace_list"},
		"open_session":          {"session_open"},
		"inspect_files":         {"source_read"},
		"inspect_git":           {"workspace_observe"},
		"modify_files":          {"change_prepare", "change_apply", "change_revert"},
		"run_tests_or_build":    {"command_run"},
		"manage_changesets":     {"change_prepare", "change_read", "change_apply", "change_revert"},
		"handle_tasks":          {"task_read", "task_control"},
		"ask_user_confirmation": {"progress_report"},
	},
}

// agentGuidance is the short, stable routing contract shown after session_open.
// It deliberately contains no workspace path, secret, confirmation token, or
// concrete tool argument value. It does include the compact change_apply
// payload cheat-sheet so agents can construct valid calls even when a host
// compresses or drops tool schemas from a long conversation context.
func agentGuidance() map[string]any {
	config := defaultAgentGuidance
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
	config := defaultAgentGuidance
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
	lines = append(lines, "", "change_prepare 参数速查（工具 schema 不可用时使用）：")
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
	setTransition := func(transition string) {
		delete(result, "action")
		result["transition"] = transition
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
		if action == "prepare" {
			tool = "change_prepare"
			delete(result, "action")
		} else {
			tool = "change_read"
			setView(action)
		}
	case "change_execute":
		if _, ok := result["revert_changeset_id"]; ok {
			tool = "change_revert"
			if value, ok := result["revert_changeset_id"]; ok {
				result["changeset_id"] = value
			}
			delete(result, "revert_changeset_id")
		} else if _, ok := result["changeset_id"]; ok {
			tool = "change_apply"
		} else {
			tool = "change_prepare"
		}
	case "command_execute":
		tool = "command_run"
	case "task_manage":
		if action == "attach" || action == "stop" || action == "stdin" {
			tool = "task_control"
			delete(result, "action")
			result["operation"] = action
		} else {
			tool = "task_read"
			setView(action)
		}
	case "plan_manage":
		switch action {
		case "create":
			tool = "plan_create"
			delete(result, "action")
		case "get":
			tool = "plan_read"
			delete(result, "action")
		default:
			tool = "plan_transition"
			setTransition(action)
		}
	case "runtime_inspect":
		tool = "runtime_read"
		setView(action)
	case "environment_inspect":
		if value, ok := result["save_snapshot"].(bool); ok && value {
			tool = "environment_snapshot_create"
			delete(result, "save_snapshot")
		} else {
			tool = "environment_read"
			if _, ok := result["compare_to"]; ok {
				result["snapshot_id"] = result["compare_to"]
				delete(result, "compare_to")
			}
			setView("current")
		}
	case "workspace_state":
		tool = "workspace_observe"
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
			tool = "artifact_register"
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
