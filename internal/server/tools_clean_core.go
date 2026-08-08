package server

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/server/prompts"
)

// cleanEditToolAnnotation reflects the edit contract: the operation changes
// files, but a successful idempotency-key replay is safe to repeat.
var cleanEditToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false,
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
	r.addTool(s, cleanCoreTool("read", desc["read"], map[string]any{
		"remote_session_id":    remoteSession,
		"view":                 enumSchema("读取视图", "file", "search", "list", "context", "environment"),
		"path":                 path,
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
		"max_bytes_per_file":   numberSchema("单文件最大字节数"),
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
			"operation":   enumSchema("文件操作", "create", "update", "delete", "rename"),
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
		"edits":             arraySchema(editItem, "跨文件批量编辑"),
		"idempotency_key":   stringSchema("同一批次重试时复用的业务幂等键"),
		"apply":             booleanSchema("是否立即应用；默认 true"),
	}, []string{"remote_session_id", "purpose", "edits"}, cleanEditToolAnnotation), r.toolEdit)

	r.addTool(s, cleanCoreTool("observe", desc["observe"], map[string]any{
		"remote_session_id": remoteSession,
		"view":              enumSchema("观察视图", "status", "history", "changes", "logs", "diff"),
		"limit":             numberSchema("返回数量限制"),
		"offset":            numberSchema("diff 视图的字节偏移"),
		"cursor":            stringSchema("分页游标"),
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
