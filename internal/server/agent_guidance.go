package server

import (
	"fmt"
	"strings"

	"mcpx/internal/envelope"
)

const agentGuidanceVersion = "1.12"

// agentGuidance is the short, stable routing contract shown after session_open.
// It deliberately contains no workspace path, secret, confirmation token, or
// concrete tool argument value. It does include the compact change_execute
// payload cheat-sheet so agents can construct valid calls even when a host
// compresses or drops tool schemas from a long conversation context.
func agentGuidance() map[string]any {
	return map[string]any{
		"version":  agentGuidanceVersion,
		"priority": "high",
		"summary":  "使用专用 MCPX 工具检查和修改；command_execute 仅用于明确要求的命令。",
		"rules": []string{
			"session_open 成功后，必须向用户展示返回的完整 remote_session_id UUID 以及绑定的 Workspace，不要只展示缩短标签。",
			"每次 MCPX 工具调用都必须提供简洁且非空的顶层 intent，说明当前目标和预期结果；缺失时会在执行前被拒绝。",
			"使用 file_read 或 context_query 检查文件和源码上下文。",
			"context_query action=list 只返回普通文件，不能证明目录完整性或目录存在；不要从嵌套文件路径推断顶层目录清单。",
			"模型或客户端需要完整预览文件时，对单个 path 调用 file_read，并设置 mode=full；图片以直接图像内容返回，HTML 和其他文本以内联文本返回。普通源码检查优先使用默认 window 模式。",
			"workspace_state 仅支持 changes、snapshot、diff、watch、memory 五种 action；不要使用不支持的 status。可用于检查 Git 状态、差异、快照、工作区变更和项目记忆。",
			"workspace_state 的 diff 和 watch 必须传入此前 snapshot 返回的 snapshot_id；快照仅属于创建它的 Remote Session，找不到时重新创建快照后再试。",
			"创建 Plan 时，为每个 tasks[].task_id 提供简短、唯一、语义稳定的 ID（如 inspect、implement、verify）。create/get 结果会返回精确 plan_id 和 task_id；start_task、complete_task、block_task、replan 只能复制这些返回值，绝不猜测。若 ID 不在当前结果中，先用 plan_manage action=get 和已知 plan_id 查询。",
			"extension_manage 的 Skill 或 MCP 名称只能使用 session_open 或 extension_manage action=list 返回的 name；目标未在前一页时使用 action=list、kind=skill、query=<名称或关键词> 筛选，未找到名称时先 list，再选择返回项，不要猜测。",
			"所有文件修改都使用 change_execute；修改或重命名前先用 file_read 获取目标文件的完整 sha256，或在 context_query 中设置 include_sha256=true；删除可省略哈希，由服务端捕获并在应用前复核。delete 可直接提交目录路径，服务端会原子隔离目录，不要枚举 .venv、缓存或 site-packages 内的文件。删除与创建不能放在同一次 change_execute：先单独完成删除并重新枚举，再单独提交创建。文件操作不要求 Workspace 是 Git 仓库。",
			"嵌套文件只提交文件操作；change_execute 会自动创建缺失的父目录，不要把目录路径作为普通文件创建。",
			"environment_inspect 用于获取主机、Python 和工具链等环境事实；不要为同一信息再调用包含 if、管道、重定向或命令替换语法的 command_execute。command_execute 仅用于用户明确要求且符合命令策略的命令、测试或构建。",
			"change_manage 仅用于准备或查看 Changeset；应用和回滚统一使用 change_execute。",
			"变更成功或审阅 Changeset 后，最终回复必须用 Markdown ```diff 代码块展示每个文件的具体增加和删除，不能只列文件名。变更过大时展示有界预览并说明 Changeset 资源。",
			"返回 need_confirmation 时，先用自然语言向用户展示命令或文件变更摘要并等待明确确认；用户确认后，使用原 changeset_id + expected_digest（或原 command + purpose）重试并设置 user_confirmed=true。若用户原始请求已明确授权本次文件变更，可在首次 operations 请求中设置 user_confirmed=true。确认始终通过原工具完成，不调用单独的审批工具。",
			"没有成功的工具结果时，不要声称文件已读取、修改或验证。",
			"重要、变更、删除或长时间调用前，先简述将做什么及原因，然后一次提交完整参数，不要等待重新获取工具 schema。",
			"每次工具调用后，在下一次非简单调用前通过顶层 progress_summary 补充已验证结果和下一步；区分事实、假设和未知。",
			"如果下一步不再调用工具（任务完成、阻塞、等待用户或需要决策），先调用 progress_report，填写已验证结果、状态和下一步。",
			"同一已验证结果只发送一次 progress_report；工具返回已包含摘要时，不要再次重复报告相同内容。",
			"全量清理优先按顶层目录整体递归删除，不要枚举 .git、.venv、缓存或 site-packages 内部文件；以返回的 delete_summary 判断删除模式和数量。",
			"每次 change_execute 最多新增 300 行（按实际插入行数计算）；不要通过截断文件绕过限制，若安全检查阻塞则使用完整内容或 replace_range 重试。",
			"不要把指令文本（提示词、guidance、AGENTS.md）写入文件内容；使用普通代码或数据。安全检查阻塞时缩小内容后重试。",
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
			"tool":           "change_execute",
			"required":       []string{"remote_session_id", "summary"},
			"user_confirmed": "用户原始请求已明确授权时可随 operations 首次提交；否则仅在用户确认后使用原 changeset_id + expected_digest 重试",
			"operations_item": map[string]any{
				"operation":     "create | update | rename | delete | replace_exact | insert_before | insert_after | delete_exact | replace_range",
				"create":        "content = 完整的新文件内容；最多新增 300 行，大文件用 insert_after 分段追加",
				"update":        "patch = apply_patch 风格的 diff 文本",
				"rename":        "path + new_path",
				"delete":        "path；支持文件或目录。base_sha256 可省略，服务端会捕获文件或目录树版本并在应用前复核；目录不应展开为逐文件删除。不要在同一 operations 中混合 delete 和 create；必须分两次 change_execute",
				"insert_after":  "path + match + content（大文件分段追加；插入完整行时 content 以换行结尾）",
				"replace":       "path + match + replacement（也可附带 occurrence）",
				"replace_range": "path + range_start + range_end + content + base_sha256（始终需要当前版本）",
				"base_sha256":   "file_read 返回的文件版本；create 可省略，delete 也可省略",
			},
			"alternatives": "changeset_id + expected_digest 应用已准备的草稿；revert_changeset_id 回滚已应用的 Changeset。三种模式互斥。",
		},
		"tool_routing": map[string]any{
			"select_workspace":      []string{"workspace_list"},
			"open_session":          []string{"session_open"},
			"inspect_files":         []string{"file_read", "context_query"},
			"inspect_git":           []string{"workspace_state"},
			"modify_files":          []string{"change_execute"},
			"run_tests_or_build":    []string{"command_execute"},
			"manage_changesets":     []string{"change_manage", "change_execute"},
			"handle_tasks":          []string{"task_manage"},
			"ask_user_confirmation": []string{"progress_report"},
		},
	}
}

func agentGuidanceRevision() string { return hashRevision(agentGuidance()) }

func agentGuidanceInstructions() string {
	guidance := agentGuidance()
	rules, _ := guidance["rules"].([]string)
	lines := []string{
		"MCPX Agent 指引（高优先级）：",
		"每个意图都使用对应的 MCPX 工具；不要用 command_execute 替代文件或 Git 检查。",
	}
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	if contract, ok := guidance["response_contract"].(map[string]any); ok {
		lines = append(lines, "", "用户可见响应契约：")
		if required, ok := contract["before_tool_call"].([]string); ok {
			lines = append(lines, "- 重要工具调用前：")
			for _, item := range required {
				lines = append(lines, "  - "+item)
			}
		}
		if required, ok := contract["after_tool_call"].([]string); ok {
			lines = append(lines, "- 每次工具调用后：")
			for _, item := range required {
				lines = append(lines, "  - "+item)
			}
		}
		if evidence, ok := contract["evidence_rule"].(string); ok {
			lines = append(lines, "- 证据规则："+evidence)
		}
	}
	if payload, ok := guidance["change_payload"].(map[string]any); ok {
		lines = append(lines, "", "change_execute 参数速查（工具 schema 不可用时使用）：")
		if required, ok := payload["required"].([]string); ok {
			lines = append(lines, "- 必填："+strings.Join(required, "、"))
		}
		if confirmed, ok := payload["user_confirmed"].(string); ok {
			lines = append(lines, "- user_confirmed："+confirmed)
		}
		if item, ok := payload["operations_item"].(map[string]any); ok {
			lines = append(lines, "- operations 项："+fmt.Sprint(item["operation"]))
			for _, key := range []string{"create", "update", "rename", "delete", "insert_after", "replace", "replace_range", "base_sha256"} {
				if value, ok := item[key].(string); ok {
					lines = append(lines, "  - "+key+"："+value)
				}
			}
		}
		if alternatives, ok := payload["alternatives"].(string); ok {
			lines = append(lines, "- 其他模式："+alternatives)
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
