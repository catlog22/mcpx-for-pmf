package server

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/edit"
	"mcpx/internal/file"
	"mcpx/internal/server/prompts"
)

// cleanEditSafetyMeta is deliberately additive metadata for MCP Hosts. It does
// not claim that a delete is harmless and cannot bypass host approval; it
// explains the bounded, auditable contract that the host can show alongside
// the standard destructiveHint.
var cleanEditSafetyMeta = mcp.Meta{
	"mcpx/safety": map[string]any{
		"classification":    "constrained_workspace_file_mutation",
		"approval":          "host_user_approval_not_used_for_delete",
		"scope":             "registered_workspace_root",
		"target":            "regular_files_only_for_create_update_rename",
		"revision_guard":    "sha256",
		"symlink_policy":    "reject",
		"idempotency":       "supported",
		"audit":             "durable",
		"execution":         "filesystem_only",
		"shell_bypass":      "forbidden",
		"approval_evidence": []string{"purpose", "explicit_paths", "base_sha256", "server_snapshot"},
		"server_rejections": []string{"path_escape", "symlink", "non_regular_file", "stale_revision", "file_policy_denied", "delete_use_remove_prepare"},
	},
}

// cleanEditToolAnnotation reflects the edit contract: the operation changes
// files, but a successful idempotency-key replay is safe to repeat. Delete is
// intentionally still marked destructive so a host can request approval.
var cleanEditToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false,
	Title: "Workspace 文件变更（不提供删除）", Meta: cleanEditSafetyMeta,
}

// cleanCoreTool keeps remote_session_id visible. The old publicTool helper
// intentionally rewrites that field to session_id for the legacy catalog;
// clean-core clients use the stable remote_session_id contract directly.
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
	readPath := stringSchema("文件路径；view=list 时是硬作用域目录/文件，不会返回作用域外结果")
	maxBytesPerFile := numberSchema(fmt.Sprintf("单文件返回字节预算；完整源文件上限为 %d bytes，超大文件请用 window", file.MaxSourceBytes))
	maxBytesPerFile["maximum"] = file.MaxSourceBytes
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
			"operation":   enumSchema("文件操作；删除请使用 remove_prepare/submit_remove", "create", "update", "rename"),
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

	deleteTarget := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":            path,
			"kind":            enumSchema("删除目标类型；file 删除普通文件，directory 删除指定目录树", "file", "directory"),
			"expected_sha256": stringSchema("文件 SHA-256；目录可留空，由 prepare 计算冻结目录树摘要"),
		},
		"required": []string{"path", "kind"},
	}
	deleteTargets := arraySchema(deleteTarget, "明确的文件/目录删除清单；服务端冻结完整目录树并分块执行")
	deleteTargets["minItems"] = 1
	deleteTargets["maxItems"] = deleteRequestMaxTargets
	r.addTool(s, cleanCoreTool("remove_prepare", "冻结已注册 Workspace 内的明确文件/目录删除清单；只读，不执行删除，返回不可变 manifest_sha256 和服务端生成的 confirmation_uuid。网页端模型必须先向用户展示清单并询问，确认后再提交。禁止 shell、glob 和 symlink 越界。", map[string]any{
		"remote_session_id": remoteSession,
		"workspace":         workspace,
		"purpose":           stringSchema("向用户展示的删除目的"),
		"targets":           deleteTargets,
		"idempotency_key":   stringSchema("同一删除准备请求重试时复用；不同清单必须使用新 key"),
	}, []string{"remote_session_id", "workspace", "purpose", "targets", "idempotency_key"}, workspaceDeletePrepareAnnotation), r.toolWorkspaceDeletePrepare)
	r.addTool(s, cleanCoreTool("submit_remove", "提交网页端模型已向用户询问并确认的、已冻结 manifest 中的 Workspace 文件/目录删除；仅接受 remove_prepare 返回的 confirmation_uuid，不接受自由路径、glob、shell 或 ARC/Host metadata。服务端会重新校验 manifest、SHA、权限和审计。", map[string]any{
		"remote_session_id": remoteSession,
		"workspace":         workspace,
		"purpose":           stringSchema("向网页端用户展示并取得明确确认的删除目的"),
		"delete_request_id": stringSchema("remove_prepare 返回的冻结删除请求 ID"),
		"manifest_sha256":   stringSchema("冻结清单的 SHA-256"),
		"confirmation_uuid": stringSchema("remove_prepare 返回的服务端生成 UUID；网页端模型向用户展示冻结清单并获得明确确认后原样带回"),
		"idempotency_key":   stringSchema("必须与 prepare 完全一致；重试时复用"),
	}, []string{"remote_session_id", "workspace", "purpose", "delete_request_id", "manifest_sha256", "confirmation_uuid", "idempotency_key"}, workspaceDeleteCommitAnnotation), r.toolWorkspaceDeleteCommit)

	r.addTool(s, cleanCoreTool("observe", desc["observe"], map[string]any{
		"remote_session_id": remoteSession,
		"workspace":         workspace,
		"view":              enumSchema("观察视图", "status", "history", "changes", "logs", "diff"),
		"limit":             numberSchema("返回数量限制"),
		"offset":            numberSchema("diff 视图的字节偏移"),
		"cursor":            stringSchema("分页游标"),
		"call_id":           stringSchema("按调用关联 ID 过滤 history"),
		"session_id":        stringSchema("按 Remote Session 过滤 history；通常使用 remote_session_id 即可"),
		"event_ids":         arraySchema(map[string]any{"type": "string"}, "按事件 sequence ID 过滤 history"),
		"request_ids":       arraySchema(map[string]any{"type": "string"}, "按多个请求 ID 过滤 history"),
		"operation_ids":     arraySchema(map[string]any{"type": "string"}, "按 Operation ID 过滤 history"),
		"task_ids":          arraySchema(map[string]any{"type": "string"}, "按 Task ID 过滤 history"),
		"changeset_ids":     arraySchema(map[string]any{"type": "string"}, "按 Changeset ID 过滤 history"),
		"keyword":           stringSchema("在摘要、用途、工具、命令、路径和输入输出中搜索 history"),
		"kinds":             arraySchema(map[string]any{"type": "string"}, "事件类型过滤，如 tool、command、skill、mcp、file_change、session、error"),
		"statuses":          arraySchema(map[string]any{"type": "string"}, "按事件状态过滤 history"),
		"created_after":     stringSchema("仅返回此时间之后的事件；支持 RFC3339、YYYY-MM-DD 或 Unix 毫秒"),
		"created_before":    stringSchema("仅返回此时间之前的事件；支持 RFC3339、YYYY-MM-DD 或 Unix 毫秒"),
		"edit_id":           stringSchema("edit 返回的变更 ID；view=diff 时使用"),
		"task_id":           stringSchema("任务 ID；view=logs 时使用"),
		"include_diff":      booleanSchema("是否包含完整 Unified Diff"),
		"path":              path,
	}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolObserve)

	// Keep tools outside the P0 convergence available until their dedicated P1
	// through P4 plans replace their public names.
	r.registerConsolidatedToolsCatalog(s, true)
	// Changeset handlers remain callable by internal operation/recovery tests and
	// existing in-process services, but are deliberately absent from toolIndex
	// and therefore never appear in the public tools/list catalog.
	if _, exists := r.toolHandlers["change"]; !exists {
		r.toolHandlers["change"] = r.instrumentTool("change", r.toolChange)
	}
	if _, exists := r.toolHandlers["change_read"]; !exists {
		r.toolHandlers["change_read"] = r.instrumentTool("change_read", r.toolChangeRead)
	}
	// Private compatibility handlers keep stored operation fixtures and older
	// in-process callers working without reintroducing legacy names into
	// tools/list, schemas, bootstrap or recovery payloads.
	for name, handler := range map[string]mcp.ToolHandler{
		"command_run":        r.toolCommandRun,
		"command_execute":    r.toolCommandExecute,
		"task_read":          r.toolTaskRead,
		"task":               r.toolTask,
		"plan_read":          r.toolPlanReadPublic,
		"extension_discover": r.toolExtensionDiscover,
		"artifact_read":      r.toolArtifactReadPublic,
	} {
		if _, exists := r.toolHandlers[name]; !exists {
			r.toolHandlers[name] = r.instrumentTool(name, handler)
		}
	}
}
