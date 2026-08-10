package server

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/edit"
	"mcpx/internal/file"
	"mcpx/internal/server/prompts"
	"mcpx/internal/source"
)

// cleanEditSafetyMeta is deliberately additive metadata for MCP Hosts. It does
// not claim that a delete is harmless and cannot bypass host approval; it
// explains the bounded, auditable contract that the host can show alongside
// the standard destructiveHint.
var cleanEditSafetyMeta = mcp.Meta{
	"mcpx/safety": map[string]any{
		"classification":    "constrained_workspace_file_mutation",
		"approval":          "web_model_user_confirmation_required_for_move_out",
		"scope":             "registered_workspace_root",
		"target":            "regular_files_only_for_create_update_rename",
		"revision_guard":    "sha256",
		"symlink_policy":    "reject",
		"idempotency":       "supported",
		"audit":             "durable",
		"execution":         "filesystem_only",
		"shell_bypass":      "forbidden",
		"approval_evidence": []string{"purpose", "explicit_paths", "base_sha256", "server_snapshot"},
		"server_rejections": []string{"path_escape", "symlink", "non_regular_file", "stale_revision", "file_policy_denied", "move_out_required"},
	},
}

// edit only supports create/update/rename inside the registered workspace.
// Removal is a separate confirmed workflow, so edit itself is non-destructive.
var cleanEditToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false,
	Title: "Workspace 文件变更（不提供删除）", Meta: cleanEditSafetyMeta,
}

var planToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"create":   riskDescriptor(false, false, true, false, "server_state_write"),
		"read":     riskDescriptor(true, false, true, false, "server_state_read"),
		"advance":  riskDescriptor(false, false, true, false, "server_state_write"),
		"complete": riskDescriptor(false, false, true, false, "server_state_write"),
		"block":    riskDescriptor(false, false, true, false, "server_state_write"),
		"replan":   riskDescriptor(false, false, true, false, "server_state_write"),
		"deliver":  riskDescriptor(false, false, true, false, "server_state_write"),
	}},
}

var artifactToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"register": riskDescriptor(false, false, true, false, "workspace_artifact_registration"),
		"list":     riskDescriptor(true, false, true, false, "workspace_artifact_read"),
		"read":     riskDescriptor(true, false, true, false, "workspace_artifact_read"),
	}},
}

func riskDescriptor(readOnly, destructive, idempotent, openWorld bool, classification string) map[string]any {
	return map[string]any{
		"read_only": readOnly, "destructive": destructive, "idempotent": idempotent,
		"open_world": openWorld, "classification": classification,
	}
}

// cleanCoreTool builds the stable clean-core contract with remote_session_id.
func cleanCoreTool(name, description string, properties map[string]any, required []string, annotation toolAnnotation) mcp.Tool {
	publicProperties := make(map[string]any, len(properties)+1)
	for key, value := range properties {
		publicProperties[key] = value
	}
	publicProperties["execution_mode"] = enumSchema("执行模式", "sync", "async")
	publicProperties["call_id"] = stringSchema("外部调用关联 ID；缺省时由 Runtime 使用 request_id")
	raw, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           publicProperties,
		"required":             required,
		"additionalProperties": false,
	})
	return annotatedTool(mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(raw)}, annotation)
}

func (r *Runtime) registerCleanCoreTools(s *mcp.Server) {
	desc := prompts.MustDescriptions()
	remoteSession := stringSchema("跨客户端复用的 Remote Session 标识")
	workspace := stringSchema("已注册的 Workspace 名称")
	path := stringSchema("Workspace 内的相对文件路径")

	r.addTool(s, cleanCoreTool("workspace", desc["workspace"], map[string]any{
		"action": enumSchema("Workspace 查询动作", "list"),
	}, []string{"action"}, readOnlyToolAnnotation), r.toolWorkspace)

	r.addTool(s, cleanCoreTool("session", desc["session"], map[string]any{
		"remote_session_id":            remoteSession,
		"action":                       enumSchema("会话生命周期动作", "open", "close", "attach"),
		"workspace":                    workspace,
		"label":                        stringSchema("会话标签"),
		"description":                  stringSchema("开发目标或会话描述"),
		"client_request_id":            stringSchema("客户端幂等键"),
		"include_instructions_content": booleanSchema("是否内联返回指令内容"),
		"include_upstream_tools":       booleanSchema("是否返回上游 MCP 工具"),
		"include_project_tasks":        booleanSchema("是否返回项目任务"),
		"known_revisions": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "客户端已知的 revision 值",
		},
		"mode": enumSchema("关闭模式", "closed", "archived"),
	}, []string{"action"}, sessionToolAnnotation), r.toolSession)

	readItems := arraySchema(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":   path,
			"mode":   enumSchema("文件读取模式", "window", "full"),
			"offset": numberSchema("0-based 行偏移"),
			"limit":  numberSchema("最大行数"),
		},
		"required": []string{"path"},
	}, "批量文件读取项")
	readItems["maxItems"] = MaxReadItems
	readItems["description"] = fmt.Sprintf("批量文件读取项；最多 %d 项；window 可读取超过单次 full 上限的源文件", MaxReadItems)
	readPath := stringSchema("文件路径；view=list 时是硬作用域目录/文件，不会返回作用域外结果；同时返回该 scope 第一层的目录、文件和 symlink 条目")
	maxBytesPerFile := numberSchema(fmt.Sprintf("单文件返回字节预算；完整源文件上限为 %d bytes，超大文件请用 window", file.MaxSourceBytes))
	maxBytesPerFile["maximum"] = file.MaxSourceBytes
	directEntriesLimit := numberSchema(fmt.Sprintf("view=list 第一层 entries 返回数量；默认 %d，最大 %d", source.DefaultDirectListEntries, source.MaxDirectListEntries))
	directEntriesLimit["maximum"] = source.MaxDirectListEntries
	r.addTool(s, cleanCoreTool("read", desc["read"], map[string]any{
		"remote_session_id":    remoteSession,
		"view":                 enumSchema("读取视图", "file", "search", "list", "context", "environment"),
		"path":                 readPath,
		"mode":                 enumSchema("文件读取模式", "window", "full"),
		"offset":               numberSchema("0-based 行偏移"),
		"limit":                numberSchema("行数或结果数量限制"),
		"items":                readItems,
		"max_total_bytes":      numberSchema("批量读取总字节预算"),
		"query":                stringSchema("搜索或上下文查询"),
		"search_mode":          enumSchema("上下文搜索模式", "smart", "exact", "token"),
		"parallel":             booleanSchema("是否并行召回"),
		"paths":                arraySchema(map[string]any{"type": "string"}, "搜索范围"),
		"include_glob":         stringSchema("包含 glob"),
		"exclude_glob":         stringSchema("排除 glob"),
		"cursor":               stringSchema("分页游标"),
		"entries_cursor":       stringSchema("view=list 的第一层 entries 分页游标；与递归 files 的 cursor 独立"),
		"entries_limit":        directEntriesLimit,
		"max_results":          numberSchema("最多匹配文件数"),
		"max_bytes_per_file":   maxBytesPerFile,
		"regex":                booleanSchema("是否按 RE2 正则解释"),
		"case_sensitive":       booleanSchema("是否区分大小写"),
		"include_sha256":       booleanSchema("是否返回文件 sha256"),
		"include_instructions": booleanSchema("是否返回适用指令"),
		"context_before":       numberSchema("匹配前上下文行数"),
		"context_after":        numberSchema("匹配后上下文行数"),
		"sections":             arraySchema(map[string]any{"type": "string"}, "环境分区"),
		"snapshot_id":          stringSchema("环境快照 ID"),
	}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolRead)

	editItem := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":        path,
			"operation":   enumSchema("文件操作；用户提出删除、移除或清理时请使用 move_out(action=prepare)，确认后再 move_out(action=submit)", "create", "update", "rename"),
			"base_sha256": stringSchema("读取时获得的文件 sha256"),
			"content":     stringSchema("新文件的完整内容"),
			"new_path":    stringSchema("rename 的目标路径"),
			"replacements": arraySchema(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"match":       stringSchema("必须精确唯一匹配的片段"),
					"replacement": stringSchema("替换后的片段"),
				},
				"required": []string{"match", "replacement"},
			}, "从后往前应用的精确替换列表"),
		},
		"required": []string{"path", "operation"},
	}
	r.addTool(s, cleanCoreTool("edit", desc["edit"], map[string]any{
		"remote_session_id": remoteSession,
		"purpose":           stringSchema("本次文件变更的用户可见目的"),
		"edits":             arraySchema(editItem, fmt.Sprintf("跨文件批量编辑；总 changed lines 上限为 %d", edit.MaxChangedLines)),
		"idempotency_key":   stringSchema("同一批次重试时复用的业务幂等键"),
		"apply":             booleanSchema("是否立即应用；默认 true"),
	}, []string{"remote_session_id", "purpose", "edits"}, cleanEditToolAnnotation), r.toolEdit)

	moveOutTarget := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":            path,
			"kind":            enumSchema("移出目标类型；file 移出普通文件，directory 原子移出指定目录树，symlink 只移出链接入口", "file", "directory", "symlink"),
			"expected_sha256": stringSchema("file 必填 SHA-256；directory 必须省略；symlink 可选，prepare 会冻结链接文本摘要"),
		},
		"required": []string{"path", "kind"},
	}
	moveOutTargets := arraySchema(moveOutTarget, "明确的文件、目录或 symlink 安全移出清单；directory 原子移出当前目录树，symlink 只移出链接入口且不跟随目标")
	moveOutTargets["minItems"] = 1
	moveOutTargets["maxItems"] = moveOutRequestMaxTargets
	moveOutCommon := map[string]any{
		"remote_session_id": remoteSession,
	}
	moveOutBranches := map[string]actionSchemaBranch{
		"prepare": {
			Description: "只读冻结明确的 Workspace 文件、目录或 symlink 安全移出清单；不执行文件系统移动。",
			Properties: map[string]any{
				"workspace":       workspace,
				"purpose":         stringSchema("向用户展示的安全移出目的；删除/移除/清理请求必须准确描述最终移出意图"),
				"targets":         moveOutTargets,
				"idempotency_key": stringSchema("同一安全移出准备请求重试时复用；不同清单必须使用新 key"),
			},
			Required: []string{"remote_session_id", "workspace", "purpose", "targets", "idempotency_key"},
		},
		"submit": {
			Description: "提交网页端模型已向用户询问并确认的冻结 manifest；客户端只能带回服务端签发的 confirmation_uuid。",
			Properties: map[string]any{
				"confirmation_uuid": stringSchema("move_out(action=prepare) 返回的服务端生成 UUID；用户确认后原样带回"),
			},
			Required: []string{"remote_session_id", "confirmation_uuid"},
		},
	}
	r.addTool(s, cleanActionTool("move_out", desc["move_out"], moveOutCommon, moveOutBranches, workspaceMoveOutToolAnnotation), r.toolMoveOut)

	r.addTool(s, cleanCoreTool("observe", desc["observe"], map[string]any{
		"remote_session_id":  remoteSession,
		"workspace":          workspace,
		"view":               enumSchema("观察视图", "session", "status", "plan", "history", "changes", "logs", "diff"),
		"limit":              numberSchema("返回数量限制"),
		"offset":             numberSchema("diff 视图的字节偏移"),
		"cursor":             stringSchema("分页游标"),
		"call_id":            stringSchema("按调用关联 ID 过滤 history"),
		"event_ids":          arraySchema(map[string]any{"type": "string"}, "按事件 sequence ID 过滤 history"),
		"request_ids":        arraySchema(map[string]any{"type": "string"}, "按多个请求 ID 过滤 history"),
		"operation_ids":      arraySchema(map[string]any{"type": "string"}, "按 Operation ID 过滤 history"),
		"plan_task_ids":      arraySchema(map[string]any{"type": "string"}, "按 Plan Task ID 过滤 history"),
		"execution_task_ids": arraySchema(map[string]any{"type": "string"}, "按执行 Task ID 过滤 history"),
		"keyword":            stringSchema("在摘要、用途、工具、命令、路径和输入输出中搜索 history"),
		"kinds":              arraySchema(map[string]any{"type": "string"}, "事件类型过滤，如 tool、command、skill、mcp、file_change、session、error"),
		"statuses":           arraySchema(map[string]any{"type": "string"}, "按事件状态过滤 history"),
		"created_after":      stringSchema("仅返回此时间之后的事件；支持 RFC3339、YYYY-MM-DD 或 Unix 毫秒"),
		"created_before":     stringSchema("仅返回此时间之前的事件；支持 RFC3339、YYYY-MM-DD 或 Unix 毫秒"),
		"edit_id":            stringSchema("edit 返回的变更 ID；view=diff 时使用"),
		"plan_task_id":       stringSchema("Plan Task ID；view=plan 时使用，也可用于 history 过滤"),
		"execution_task_id":  stringSchema("执行 Task ID；view=status/logs 时使用，也可用于 history 过滤"),
		"include_diff":       booleanSchema("是否包含完整 Unified Diff"),
		"path":               path,
	}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolObserve)

	r.addTool(s, cleanCoreTool("progress", desc["progress"], map[string]any{
		"remote_session_id": remoteSession,
		"status":            enumSchema("模型当前工作状态；停止 MCPX 工具调用前必须使用终态 completed、waiting_for_user、blocked 或 failed", "in_progress", "completed", "waiting_for_user", "blocked", "failed"),
		"current":           stringSchema("当前阶段或终态的用户可见摘要；陈述已发生或正在发生的工作"),
		"result":            stringSchema("已验证结果摘要；没有证据时不要声称已完成或通过"),
		"next":              stringSchema("下一步动作；completed 时通常留空"),
		"phase":             stringSchema("可选的阶段名称，例如 implementation、verification、release"),
		"related_tool":      stringSchema("可选的相关 MCPX 工具名"),
	}, []string{"remote_session_id", "current"}, sessionToolAnnotation), r.toolProgress)

	r.registerConsolidatedToolsCatalog(s)
}
