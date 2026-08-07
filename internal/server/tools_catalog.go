package server

import (
	"encoding/json"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/environment"
	"mcpx/internal/operation"
)

// registerTools is the sole public tool registration point. The legacy
// fine-grained handlers remain private implementation details behind the
// explicit public catalog below.
func (r *Runtime) registerTools(s *mcp.Server) {
	r.registerConsolidatedToolsV2(s)
	r.captureToolIndex(s)
}

func (r *Runtime) captureToolIndex(s *mcp.Server) {
	// Official go-sdk has no ListTools snapshot API; addTool fills toolIndex.
	_ = s
}

func (r *Runtime) registeredTools() []mcp.Tool {
	r.toolIndexMu.RLock()
	defer r.toolIndexMu.RUnlock()
	tools := make([]mcp.Tool, 0, len(r.toolIndex))
	for _, tool := range r.toolIndex {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// listedToolMap is a test/helper snapshot of the registered tool catalog.
func (r *Runtime) listedToolMap() map[string]mcp.Tool {
	r.toolIndexMu.RLock()
	defer r.toolIndexMu.RUnlock()
	out := make(map[string]mcp.Tool, len(r.toolIndex))
	for name, tool := range r.toolIndex {
		out[name] = tool
	}
	return out
}

// currentToolSchemaRevision is derived from the actual MCP registration,
// including name, description, input schema, and annotations. It deliberately
// excludes Session state so opening or handing off a session cannot refresh a
// client's tools/list cache.
func (r *Runtime) currentToolSchemaRevision() string {
	tools := r.registeredTools()
	items := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded, _ := json.Marshal(tool)
		var item map[string]any
		_ = json.Unmarshal(encoded, &item)
		items = append(items, item)
	}
	return hashRevision(items)
}

// compactToolResult is the unified success-path tool result builder.
//
//	content[0].text   — human summary only
//	structuredContent — machine wire {status, data} (or an already-formed wire map)
//
// Models must consume structuredContent after ARC wrap; hosts render the text.
func compactToolResult(data any, summary string) *mcp.CallToolResult {
	if summary == "" {
		summary = "succeeded"
	}
	var wire map[string]any
	if existing, ok := asPublicWireEnvelope(data); ok {
		wire = existing
	} else {
		wire = map[string]any{
			"status": string(envelope.StatusOK),
			"data":   data,
		}
	}
	// JSON-normalize so nested slices are []any (stable for tests and hosts).
	return mcpresult.NewStructured(jsonNormalizeMap(wire), summary)
}

func jsonNormalizeMap(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return value
	}
	return normalized
}

// asPublicWireEnvelope detects handler payloads already in the public wire shape
// {status, data?, error?} so success helpers do not double-wrap them.
func asPublicWireEnvelope(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	status, _ := m["status"].(string)
	switch status {
	case string(envelope.StatusOK), string(envelope.StatusAccepted),
		string(envelope.StatusNeedConfirmation), string(envelope.StatusInterrupted),
		string(envelope.StatusError):
		if _, hasData := m["data"]; hasData {
			return m, true
		}
		if _, hasError := m["error"]; hasError {
			return m, true
		}
	}
	return nil, false
}

type toolAnnotation struct {
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

var (
	readOnlyToolAnnotation = toolAnnotation{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}
	mutatingToolAnnotation = toolAnnotation{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: true}
	sessionToolAnnotation  = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
	secretToolAnnotation   = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
	// commandExecutionToolAnnotation: whether a command is destructive is
	// decided by the server-side command policy, not by the tool itself, so
	// hosts must not gate the call on the destructive hint.
	commandExecutionToolAnnotation = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: true}
)

func annotatedTool(tool mcp.Tool, annotation toolAnnotation) mcp.Tool {
	dest, open := annotation.Destructive, annotation.OpenWorld
	tool.Annotations = &mcp.ToolAnnotations{
		ReadOnlyHint:    annotation.ReadOnly,
		DestructiveHint: &dest,
		IdempotentHint:  annotation.Idempotent,
		OpenWorldHint:   &open,
	}
	return tool
}

type actionSchemaBranch struct {
	Description string
	Properties  map[string]any
	Required    []string
}

func actionTool(name, description string, common map[string]any, branches map[string]actionSchemaBranch) mcp.Tool {
	return actionToolWithAnnotation(name, description, common, branches, mutatingToolAnnotation)
}

func actionToolWithAnnotation(name, description string, common map[string]any, branches map[string]actionSchemaBranch, annotation toolAnnotation) mcp.Tool {
	actions := make([]string, 0, len(branches))
	for action := range branches {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	properties := map[string]any{"action": map[string]any{"type": "string", "enum": actions}}
	for key, value := range common {
		properties[key] = value
	}
	oneOf := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		branch := branches[action]
		for key, value := range branch.Properties {
			if _, exists := properties[key]; !exists {
				properties[key] = value
			}
		}
		branchProperties := map[string]any{"action": map[string]any{"const": action}}
		for key, value := range branch.Properties {
			branchProperties[key] = value
		}
		required := append([]string{"action"}, branch.Required...)
		branchSchema := map[string]any{
			"properties": branchProperties,
			"required":   required,
		}
		description := branch.Description
		if description == "" {
			description = "仅执行「" + action + "」操作；失败时按返回的 next_action 继续。"
		}
		branchSchema["description"] = description
		oneOf = append(oneOf, branchSchema)
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": []string{"action"}, "oneOf": oneOf,
	})
	tool := annotatedTool(mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(raw)}, annotation)
	return tool
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numberSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func arraySchema(items map[string]any, description string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": description}
}

func enumSchema(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": description}
}

func publicTool(name, description string, properties map[string]any, required []string, annotation toolAnnotation) mcp.Tool {
	publicProperties := make(map[string]any, len(properties))
	for key, value := range properties {
		if key == "remote_session_id" {
			key = "session_id"
		}
		publicProperties[key] = value
	}
	publicProperties["execution_mode"] = enumSchema("执行模式", "sync", "async")
	publicRequired := make([]string, 0, len(required))
	for _, key := range required {
		if key == "remote_session_id" {
			key = "session_id"
		}
		publicRequired = append(publicRequired, key)
	}
	raw, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           publicProperties,
		"required":             publicRequired,
		"additionalProperties": false,
	})
	tool := annotatedTool(mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(raw)}, annotation)
	return tool
}

func changeOperationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "单条文件操作。字段按 operation 选用，勿混用别名：" +
			"replace_exact 用 match+replacement；insert_before/insert_after 用 match+content；delete_exact 只用 match；" +
			"replace_range 用 range_start+range_end+content（或 replacement）；update 用 patch；create 用 content；rename 用 new_path。",
		"properties": map[string]any{
			"operation": map[string]any{
				"type": "string",
				"enum": []string{"update", "create", "rename", "delete", "replace_exact", "insert_before", "insert_after", "delete_exact", "replace_range"},
				"description": "操作类型（必填）。" +
					"优先 replace_exact/insert_before/insert_after/delete_exact/replace_range；" +
					"update=标准 unified diff（勿用 *** Begin Patch）；" +
					"create=新建文件（自动建父目录，path 必须是文件不是目录）；" +
					"delete=删文件或目录；rename=path→new_path。" +
					"同一 path 可链式多个 exact/update；create/delete/rename 每个 path 仅一次。",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "工作区相对路径（必填）。rename 时为源路径。",
			},
			"new_path": map[string]any{
				"type":        "string",
				"description": "仅 operation=rename：目标路径。",
			},
			"base_sha256": map[string]any{
				"type": "string",
				"description": "文件版本指纹（乐观锁）。" +
					"取值：原样复制 source_read(view=file) 返回的字段 sha256（形如 sha256:<64位hex>），禁止自造、截断或改字段名。" +
					"必填：replace_exact、insert_before、insert_after、delete_exact、replace_range、update、rename；" +
					"同 path 链式多个 exact/update：每个 op 都填 base_sha256，且应复用该文件 source_read 的同一 sha256（链根）；服务端在批次内按序应用，不要因第 3+ 个 op 换 hash。" +
					"可省略：create；delete（服务端捕获当前版本）。" +
					"expected_sha256 是内部别名，模型请只填 base_sha256。",
			},
			"patch": map[string]any{
				"type": "string",
				"description": "仅 operation=update：基于当前 base_sha256 的局部 unified diff（--- a/<path> 与 @@ 头）。" +
					"上下文行必须来自 source_read 原文。不确定时改用 replace_exact/insert_*，不要猜 hunk。",
			},
			"content": map[string]any{
				"type": "string",
				"description": "写入正文。" +
					"create：完整新文件内容；" +
					"insert_before/insert_after：插入文本（整行插入时以换行结尾）；" +
					"replace_range：替换行范围后的新内容。" +
					"不要用于 replace_exact（replace_exact 用 replacement）。" +
					"每次最多新增 300 行，大文件用 create + insert_after 分段追加；不要写入指令文本。",
			},
			"match": map[string]any{
				"type": "string",
				"description": "精确锚点文本（replace_exact/insert_before/insert_after/delete_exact 必填）。" +
					"必须是 source_read 返回 content 中的连续原文（含空白与换行，与 format.line_ending 一致）。" +
					"须在文件中唯一匹配；否则加长 match 或失败 AMBIGUOUS。",
			},
			"replacement": map[string]any{
				"type": "string",
				"description": "仅 replace_exact：替换 match 的新文本（也可用 content 作为同义字段，优先填 replacement）。" +
					"replace_range 也可填 replacement 代替 content。",
			},
			"occurrence": map[string]any{
				"type":        "string",
				"enum":        []string{"one"},
				"description": "匹配策略：仅 one（默认）= 全文恰好 1 处 match；多处会失败，请加长 match。",
			},
			"range_start": map[string]any{
				"type":        "number",
				"description": "仅 replace_range：起始行号，从 1 开始（含）。",
			},
			"range_end": map[string]any{
				"type":        "number",
				"description": "仅 replace_range：结束行号（含）。",
			},
		},
		"required": []string{"operation", "path"},
	}
}

func changeExecuteInputSchema() map[string]any {
	operations := changeOperationSchema()
	return map[string]any{
		"type":        "object",
		"description": "唯一的文件修改入口，支持三种互斥模式：operations 创建 Changeset；changeset_id + expected_digest 应用已准备的草稿；revert_changeset_id 回滚已应用的 Changeset。用户原始请求已明确授权时，operations 可直接设置 user_confirmed=true；否则先展示摘要，确认后用原 changeset_id + expected_digest 并设置 user_confirmed=true 重试。删除与创建不能放在同一次调用，必须先删除并确认成功、重新读取工作区，再创建。每次调用只能选择一种模式。",
		"properties": map[string]any{
			"remote_session_id":   stringSchema("持久化的 Remote Session 标识"),
			"summary":             stringSchema("变更摘要"),
			"operations":          map[string]any{"type": "array", "description": "新建变更时必填的文件操作；每次最多新增 300 行，大文件用 create + insert_after 分段追加；不要写入指令文本。同一 path 可提交多个精确操作（replace_exact/insert_*/delete_exact/replace_range/update）按提交顺序依次应用；create/delete/rename 每个 path 只能出现一次", "items": operations, "minItems": 1},
			"changeset_id":        stringSchema("通过本入口应用的已准备 Changeset 草稿 ID"),
			"expected_digest":     stringSchema("必须原样复制准备 Changeset 返回的 digest；不要填写 +211 −0 等 diff 统计、tree_digest、snapshot ID 或空值"),
			"revert_changeset_id": stringSchema("通过本入口回滚的已应用 Changeset ID"),
			"idempotency_key":     stringSchema("重试同一变更时使用的业务幂等键"),
			"user_confirmed":      booleanSchema("用户已在对话中明确确认本次变更；原始请求已明确授权时可随 operations 首次提交，否则在原 changeset_id + expected_digest 重试时设置为 true"),
			"apply":               booleanSchema("准备后立即应用；默认 true"),
			"format":              booleanSchema("格式化已变更文件"),
			"verify":              arraySchema(map[string]any{"type": "string"}, "验证步骤：format、typecheck、lint 或 related_tests"),
		},
		"required": []string{"remote_session_id"},
	}
}

// registerConsolidatedTools was the pre-v2 catalog and is no longer registered.
func (r *Runtime) registerConsolidatedTools(s *mcp.Server) {
	_ = s
}

// registerConsolidatedToolsV2 is the public catalog. The older registration
// function above is retained as a source-level migration aid only; it is not
// called by registerTools and therefore does not expand tools/list.
func (r *Runtime) registerConsolidatedToolsV2(s *mcp.Server) {
	remoteSession := stringSchema("持久化的 Remote Session 标识")
	workspace := stringSchema("已注册的 Workspace 名称")
	path := stringSchema("工作区相对路径")
	changeItems := arraySchema(changeOperationSchema(), "文件操作列表")

	r.addTool(s, publicTool("workspace_list", "列出已注册的 Workspace。", map[string]any{}, nil, readOnlyToolAnnotation), r.toolWorkspaceList)
	r.addTool(s, publicTool("workspace_observe", "读取工作区变更、快照、差异、监听结果或工作区记忆；view 明确决定返回类型。", map[string]any{
		"remote_session_id": remoteSession, "view": enumSchema("读取视图", "changes", "snapshot", "diff", "watch", "memory"), "include_diff": booleanSchema("差异视图是否包含 Unified Diff"), "since": stringSchema("由 snapshot 返回的快照 ID"),
		"keyword": stringSchema("记忆关键词"), "id": stringSchema("记忆 ID 或范围"), "time": stringSchema("记忆日期或日期范围"), "latest": numberSchema("最新记忆条数"),
	}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolWorkspaceObserve)
	r.addTool(s, publicTool("workspace_history_read", "按 ID、时间、关键词、类型和状态查询工作区运行历史；多个字段同时提供时按 AND 过滤。", map[string]any{
		"workspace": workspace, "remote_session_id": remoteSession, "session_id": stringSchema("Remote Session ID"),
		"event_ids": arraySchema(map[string]any{"type": "string"}, "事件 ID"), "request_ids": arraySchema(map[string]any{"type": "string"}, "请求 ID"), "operation_ids": arraySchema(map[string]any{"type": "string"}, "操作 ID"), "task_ids": arraySchema(map[string]any{"type": "string"}, "Task ID"), "changeset_ids": arraySchema(map[string]any{"type": "string"}, "Changeset ID"),
		"created_after": stringSchema("RFC3339 或毫秒时间戳"), "created_before": stringSchema("RFC3339 或毫秒时间戳"), "keyword": stringSchema("摘要、用途、工具、Skill、MCP、路径或错误关键词"),
		"kinds": arraySchema(map[string]any{"type": "string"}, "事件类型"), "statuses": arraySchema(map[string]any{"type": "string"}, "事件状态"), "limit": numberSchema("返回数量"), "cursor": stringSchema("分页游标"),
	}, nil, readOnlyToolAnnotation), r.toolWorkspaceHistoryRead)

	operationSteps := arraySchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":         stringSchema("批次内唯一的步骤 ID"),
			"tool":       stringSchema("已注册的公开工具名称"),
			"arguments":  map[string]any{"type": "object", "additionalProperties": true, "description": "目标工具的业务参数"},
			"depends_on": arraySchema(map[string]any{"type": "string"}, "前置步骤 ID"),
		},
		"required": []string{"id", "tool", "arguments"},
	}, "带依赖关系的公开工具操作")
	r.addTool(s, publicTool("operation_batch", "提交多个公开工具操作；无依赖步骤并发执行，有依赖步骤按 DAG 顺序执行。", map[string]any{
		"remote_session_id": remoteSession, "operations": operationSteps, "purpose": stringSchema("本次调用的目的；必须由用户明确提供"),
	}, []string{"remote_session_id", "purpose", "operations"}, mutatingToolAnnotation), r.toolOperationBatch)
	operationIDsSchema := arraySchema(map[string]any{"type": "string"}, "批量查询的异步操作 ID；最多 32 个，不要把 operation_manage 嵌套进 operation_batch")
	operationIDsSchema["minItems"] = 1
	operationIDsSchema["maxItems"] = operation.MaxBatchQueries
	operationManage := publicTool("operation_manage", "查询、等待、读取结果、取消或恢复异步操作。单操作传 operation_id；批量查询 status/result 直接传 operation_ids，不要嵌套进 operation_batch。", map[string]any{
		"remote_session_id": remoteSession,
		"operation_id":      stringSchema("单个异步操作 ID；与 operation_ids 二选一"), "operation_ids": operationIDsSchema,
		"action":  enumSchema("操作动作；operation_ids 批量模式只支持 status、result", "status", "wait", "result", "cancel", "resume"),
		"step_id": stringSchema("批量操作子步骤 ID"), "timeout_ms": numberSchema("wait 最长等待毫秒数"),
		"confirmation_token": stringSchema("仅表示用户已确认同一子操作，不是认证凭据"), "cursor": stringSchema("结果分页游标"), "limit": numberSchema("结果字节或列表数量限制"),
	}, []string{"remote_session_id", "action"}, sessionToolAnnotation)
	var operationManageSchema map[string]any
	_ = json.Unmarshal(mcpresult.ToolSchemaJSON(operationManage), &operationManageSchema)
	operationManageSchema["oneOf"] = []any{
		map[string]any{
			"type":        "object",
			"description": "单操作模式；支持 status、wait、result、cancel、resume。",
			"properties": map[string]any{
				"action": enumSchema("单操作动作", "status", "wait", "result", "cancel", "resume"),
			},
			"required": []string{"operation_id"},
		},
		map[string]any{
			"type":        "object",
			"description": "批量查询模式；仅支持 status、result，直接传 operation_ids。",
			"properties": map[string]any{
				"action": enumSchema("批量查询动作", "status", "result"),
			},
			"required": []string{"operation_ids"},
		},
	}
	operationManageSchema["required"] = []string{"session_id", "action"}
	operationManage.InputSchema = mustSchemaJSON(operationManageSchema)
	r.addTool(s, operationManage, r.toolOperationManage)

	r.addTool(s, publicTool("session_open", "创建或恢复 Remote Session，并返回启动上下文。", map[string]any{
		"remote_session_id": remoteSession, "workspace": workspace, "label": stringSchema("会话标签"), "description": stringSchema("开发目标"), "client_request_id": stringSchema("客户端幂等键"),
		"include_instructions_content": booleanSchema("返回有界 AGENTS.md 内容"), "include_upstream_tools": booleanSchema("返回上游工具 schema"), "include_project_tasks": booleanSchema("返回项目任务"),
		"known_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "客户端已知版本"},
	}, nil, sessionToolAnnotation), r.toolSessionOpen)
	r.addTool(s, publicTool("session_read", "读取 Remote Session 列表、摘要或事件；view 明确决定返回类型。", map[string]any{
		"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图", "list", "summary", "events"), "query": stringSchema("会话查询条件"), "status": stringSchema("会话状态"), "cursor": stringSchema("分页游标"), "limit": numberSchema("分页数量"), "after_sequence": numberSchema("事件起始序号"),
	}, []string{"view"}, readOnlyToolAnnotation), r.toolSessionRead)
	r.addTool(s, publicTool("session_transition", "更新、接力、接入或关闭 Remote Session；operation 明确决定生命周期变化。", map[string]any{
		"remote_session_id": remoteSession, "operation": enumSchema("生命周期操作", "update", "handoff", "attach", "close"), "label": stringSchema("新标签"), "description": stringSchema("新描述"), "status": stringSchema("新状态"), "expected_version": numberSchema("乐观锁版本"), "role": stringSchema("接力角色"), "expires_in": numberSchema("接力有效秒数"), "note": stringSchema("接力备注"), "handoff_token": stringSchema("一次性接力令牌"), "mode": stringSchema("closed 或 archived"),
	}, []string{"remote_session_id", "operation"}, sessionToolAnnotation), r.toolSessionTransition)

	r.addTool(s, publicTool("source_read", "读取文件、搜索内容、列举文件或组装有界上下文；view 明确决定读取语义。view=file 时响应含 path、content、sha256、format（line_ending 等）；修改文件前必须用返回的 sha256 原样填入 change_prepare.operations[].base_sha256。", map[string]any{
		"remote_session_id": remoteSession, "view": enumSchema("读取视图", "file", "search", "list", "context"), "path": path, "mode": enumSchema("文件读取模式；改文件前推荐 mode=full 一次读完整文件并拿到 sha256；mode=full 一次仅一个 path，不能与 items 同用", "window", "full"), "offset": numberSchema("行偏移"), "limit": numberSchema("结果或行数限制"),
		"items": arraySchema(map[string]any{"type": "object", "properties": map[string]any{"path": path, "offset": numberSchema("行偏移"), "limit": numberSchema("行数")}, "required": []string{"path"}}, "批量文件读取项；仅 window 模式使用"), "max_total_bytes": numberSchema("批量读取总预算"),
		"query": stringSchema("搜索条件"), "search_mode": enumSchema("搜索模式", "smart", "exact", "token"), "parallel": booleanSchema("是否并行召回"), "paths": arraySchema(map[string]any{"type": "string"}, "搜索范围"), "include_glob": stringSchema("包含 glob"), "exclude_glob": stringSchema("排除 glob"), "cursor": stringSchema("分页游标"), "max_results": numberSchema("最多匹配文件数"), "max_bytes_per_file": numberSchema("单文件预算"), "regex": booleanSchema("按 RE2 正则解释"), "case_sensitive": booleanSchema("区分大小写"), "include_sha256": booleanSchema("list/search 是否附带 sha256；view=file 始终返回 sha256，无需猜字段名"), "include_instructions": booleanSchema("返回适用指令"), "context_before": numberSchema("匹配前上下文行数"), "context_after": numberSchema("匹配后上下文行数"),
	}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolSourceRead)

	r.addTool(s, publicTool("change_prepare", "校验文件操作并生成 Changeset 草稿。改已有文件：先 source_read 取 sha256→operations[].base_sha256（原样复制），再提交；默认不应用，apply=true 可同次应用。失败时看 error.code 与 details（failed_ordinal/path/field_map/next_action），勿猜测字段名。", map[string]any{"remote_session_id": remoteSession, "summary": stringSchema("变更摘要"), "operations": changeItems, "idempotency_key": stringSchema("业务幂等键"), "apply": booleanSchema("是否在准备成功后立即应用；默认 false，需要用户确认时仍返回 confirmation_token"), "format": booleanSchema("应用前格式化变更文件"), "verify": arraySchema(map[string]any{"type": "string"}, "应用后验证步骤"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "operations", "purpose"}, mutatingToolAnnotation), r.toolChangePreparePublic)
	r.addTool(s, publicTool("change_read", "读取 Changeset 差异或历史记录；view 明确决定读取类型。history 返回 history_digest；后续仅在变更状态可能变化时复用该 digest 查询，已知 digest 相同时返回 not_modified。", map[string]any{"remote_session_id": remoteSession, "view": enumSchema("读取视图", "diff", "history"), "changeset_id": stringSchema("Changeset ID"), "limit": numberSchema("历史数量"), "known_history_digest": stringSchema("上一次 history 返回的 history_digest；相同则返回 not_modified")}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolChangeRead)
	r.addTool(s, publicTool("change_discard", "丢弃尚未应用的 Changeset 草稿，不修改工作区文件；用于清理失败重试或已改方案的旧草稿。", map[string]any{"remote_session_id": remoteSession, "changeset_id": stringSchema("待丢弃的 draft Changeset ID"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "changeset_id", "purpose"}, mutatingToolAnnotation), r.toolChangeDiscardPublic)
	r.addTool(s, publicTool("change_apply", "应用已准备的 Changeset；需要语义确认时使用返回的 confirmation_token 重试。", map[string]any{"remote_session_id": remoteSession, "changeset_id": stringSchema("已准备 Changeset ID；必须原样复制 change_prepare/change_read 返回的字段 changeset_id"), "expected_digest": stringSchema("必须原样复制 change_prepare/change_read 返回的字段 digest（或 expected_digest）；禁止填 diff 统计、tree_digest、snapshot ID、changeset_id 或空值"), "confirmation_token": stringSchema("仅表示用户已确认同一变更，不是认证凭据"), "format": booleanSchema("格式化变更文件"), "verify": arraySchema(map[string]any{"type": "string"}, "验证步骤"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "changeset_id", "expected_digest", "purpose"}, mutatingToolAnnotation), r.toolChangeApply)
	r.addTool(s, publicTool("change_revert", "回滚已应用的 Changeset；需要语义确认时使用返回的 confirmation_token 重试。", map[string]any{"remote_session_id": remoteSession, "changeset_id": stringSchema("待回滚 Changeset ID；必须原样复制已应用 Changeset 的 ID"), "confirmation_token": stringSchema("仅表示用户已确认同一回滚，不是认证凭据"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "changeset_id", "purpose"}, mutatingToolAnnotation), r.toolChangeRevertPublic)

	r.addTool(s, publicTool("command_run", "按工作区命令策略执行用户要求的命令或项目任务；长命令返回 Task ID。", map[string]any{"remote_session_id": remoteSession, "command": stringSchema("用户要求执行的 shell 命令"), "task": stringSchema("已发现的项目任务名称"), "purpose": stringSchema("用户要求执行此命令的原因"), "scope": enumSchema("执行范围", "workspace"), "confirmation_token": stringSchema("仅表示用户已确认同一命令，不是认证凭据"), "yield_time_ms": numberSchema("等待时长")}, []string{"remote_session_id", "purpose"}, commandExecutionToolAnnotation), r.toolCommandRun)
	r.addTool(s, publicTool("task_read", "读取 Task 列表、状态、日志、端口或诊断信息；view 明确决定读取类型。已知 task_id 时直接查询 status/logs，不要先 list；list 返回 task_list_digest，带回相同摘要时返回 not_modified。", map[string]any{"remote_session_id": remoteSession, "view": enumSchema("读取视图", "list", "status", "logs", "ports", "diagnostics"), "task_id": stringSchema("Task ID"), "known_task_digest": stringSchema("上一次 Task 列表返回的 task_list_digest；相同则返回 not_modified"), "stdout_offset": numberSchema("stdout 偏移"), "stderr_offset": numberSchema("stderr 偏移"), "limit": numberSchema("数量限制"), "yield_time_ms": numberSchema("等待时长")}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolTaskRead)
	r.addTool(s, publicTool("task_control", "接入、停止或向运行中的 Task 写入 stdin；operation 明确决定控制动作。", map[string]any{"remote_session_id": remoteSession, "operation": enumSchema("控制操作", "attach", "stop", "stdin"), "task_id": stringSchema("Task ID"), "stdout_offset": numberSchema("stdout 偏移"), "stderr_offset": numberSchema("stderr 偏移"), "yield_time_ms": numberSchema("等待时长"), "force": booleanSchema("强制终止"), "input": stringSchema("stdin 文本"), "confirmation_token": stringSchema("仅表示用户已确认控制动作"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "operation", "task_id", "purpose"}, mutatingToolAnnotation), r.toolTaskControl)
	r.addTool(s, publicTool("progress_report", "记录模型可公开的阶段、当前动作、下一步和验证证据；不要写入隐藏思维链。", map[string]any{"remote_session_id": remoteSession, "phase": enumSchema("公开阶段", "planning", "executing", "verifying", "waiting", "blocked", "completed"), "current": stringSchema("当前已公开且可验证的动作"), "next": stringSchema("下一步或待用户回答的问题"), "evidence": arraySchema(map[string]any{"type": "string"}, "已获得的验证证据"), "status": enumSchema("进度状态", "in_progress", "completed", "waiting_for_user", "blocked"), "related_tool": stringSchema("关联工具")}, []string{"remote_session_id", "phase", "current"}, sessionToolAnnotation), r.toolProgressReportPublic)

	r.addTool(s, publicTool("plan_create", "创建持久化开发计划；plan_id 与各任务的 task_id 由服务端生成并在返回值中原样提供。", map[string]any{"remote_session_id": remoteSession, "goal": stringSchema("计划目标"), "summary": stringSchema("计划摘要"), "tasks": arraySchema(planTaskInputSchema(), "有序计划任务；task_id 仅作局部依赖引用，最终 task_id 以返回值为准"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "goal", "tasks", "purpose"}, mutatingToolAnnotation), r.toolPlanCreatePublic)
	r.addTool(s, publicTool("plan_read", "读取持久化开发计划及任务状态。", map[string]any{"remote_session_id": remoteSession, "plan_id": stringSchema("Plan ID")}, []string{"remote_session_id", "plan_id"}, readOnlyToolAnnotation), r.toolPlanReadPublic)
	r.addTool(s, publicTool("plan_transition", "推进计划任务、重新规划或交付计划；transition 明确决定状态变化。", map[string]any{"remote_session_id": remoteSession, "transition": enumSchema("计划转移", "start_task", "complete_task", "block_task", "replan", "deliver"), "plan_id": stringSchema("Plan ID"), "task_id": stringSchema("计划任务 ID"), "evidence": arraySchema(planEvidenceSchema(), "完成或阻塞证据"), "reason": stringSchema("阻塞或重新规划原因"), "goal": stringSchema("更新后的目标"), "summary": stringSchema("更新后的摘要"), "operations": arraySchema(planOperationSchema(), "计划任务操作"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "transition", "plan_id", "purpose"}, mutatingToolAnnotation), r.toolPlanTransition)

	r.addTool(s, publicTool("runtime_read", "读取能力、项目摘要或适用指令；view 明确决定读取内容。", map[string]any{"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图", "capabilities", "project", "instructions"), "include_tool_schemas": booleanSchema("返回工具 schema"), "include_skill_details": booleanSchema("返回 Skill 详情"), "known_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "anchor_path": stringSchema("指令锚点路径"), "paths": arraySchema(map[string]any{"type": "string"}, "指令路径")}, []string{"view"}, readOnlyToolAnnotation), r.toolRuntimeRead)
	r.addTool(s, publicTool("environment_read", "读取当前运行环境或与已有环境快照比较。", map[string]any{"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图", "current", "compare"), "sections": arraySchema(map[string]any{"type": "string", "enum": environment.ValidSections}, "环境分区"), "snapshot_id": stringSchema("用于比较的环境快照 ID")}, []string{"view"}, readOnlyToolAnnotation), r.toolEnvironmentRead)
	r.addTool(s, publicTool("environment_snapshot_create", "保存当前 Workspace 环境快照，供后续诊断和比较。", map[string]any{"remote_session_id": remoteSession, "sections": arraySchema(map[string]any{"type": "string", "enum": environment.ValidSections}, "环境分区")}, []string{"remote_session_id"}, sessionToolAnnotation), r.toolEnvironmentSnapshotCreate)

	r.addTool(s, publicTool("extension_discover", "发现或描述已配置的 Skill 与上游 MCP；kind、view 明确决定发现对象和结果类型。", map[string]any{"workspace": workspace, "remote_session_id": remoteSession, "kind": enumSchema("扩展类型", "skill", "mcp"), "view": enumSchema("发现视图", "list", "describe"), "query": stringSchema("名称或描述关键词"), "name": stringSchema("Skill 或 MCP 名称"), "server": stringSchema("MCP Server 名称"), "include_tools": booleanSchema("返回上游工具 schema")}, []string{"kind", "view"}, readOnlyToolAnnotation), r.toolExtensionDiscover)
	r.addTool(s, publicTool("skill_call", "调用已发现的 Skill；Skill 名称必须来自 extension_discover。", map[string]any{"remote_session_id": remoteSession, "name": stringSchema("Skill 名称"), "arguments": map[string]any{"type": "object", "additionalProperties": true}, "confirmation_token": stringSchema("仅表示用户已确认同一 Skill 调用"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "name", "purpose"}, commandExecutionToolAnnotation), r.toolSkillCall)
	r.addTool(s, publicTool("mcp_call", "调用已发现的上游 MCP 工具；Server 和工具必须来自 extension_discover。", map[string]any{"remote_session_id": remoteSession, "server": stringSchema("MCP Server 名称"), "tool": stringSchema("上游工具名称"), "arguments": map[string]any{"type": "object", "additionalProperties": true}, "confirmation_token": stringSchema("仅表示用户已确认同一 MCP 调用"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "server", "tool", "purpose"}, commandExecutionToolAnnotation), r.toolMCPCallPublic)

	r.addTool(s, publicTool("artifact_read", "读取 Remote Session 产物列表或内容；view 明确决定读取类型。", map[string]any{"remote_session_id": remoteSession, "view": enumSchema("读取视图", "list", "content"), "kind": stringSchema("产物类型"), "artifact_id": stringSchema("产物 ID"), "offset": numberSchema("字节偏移"), "limit": numberSchema("字节数量")}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolArtifactReadPublic)
	r.addTool(s, publicTool("artifact_register", "登记工作区文件为 Remote Session 产物。", map[string]any{"remote_session_id": remoteSession, "path": path, "name": stringSchema("显示名称"), "kind": stringSchema("产物类型"), "mime_type": stringSchema("MIME 类型")}, []string{"remote_session_id", "path"}, mutatingToolAnnotation), r.toolArtifactRegister)
	r.addTool(s, publicTool("screenshot_capture", "截取显示器或屏幕区域供视觉检查。", map[string]any{"remote_session_id": remoteSession, "mode": stringSchema("全屏或区域"), "display": numberSchema("显示器索引"), "x": numberSchema("区域 X"), "y": numberSchema("区域 Y"), "width": numberSchema("区域宽度"), "height": numberSchema("区域高度"), "compression": stringSchema("压缩模式"), "format": stringSchema("png 或 jpeg"), "quality": numberSchema("JPEG 质量"), "max_width": numberSchema("输出宽度上限"), "max_height": numberSchema("输出高度上限")}, []string{"remote_session_id"}, readOnlyToolAnnotation), r.toolScreenshotCapture)
	r.addTool(s, publicTool("secret_provide", "提供仅驻留内存的 Secret 值或引用，不把 Secret 写入结果或日志。", map[string]any{"remote_session_id": remoteSession, "secret_id": stringSchema("待处理 Secret ID"), "values": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Secret 名称和值，仅驻留内存"}}, []string{"remote_session_id"}, secretToolAnnotation), r.toolSecretsProvide)

	r.registerResources(s)
}

func (r *Runtime) registerResources(s *mcp.Server) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}",
		Name:        "Remote Session 产物",
		Description: "读取已注册的 MCPX 开发产物",
	}, r.resourceArtifact)
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "mcpx://remote-sessions/{remote_session_id}/changesets/{changeset_id}/diff",
		Name:        "Changeset Unified Diff",
		Description: "读取 MCPX Changeset 的完整 Unified Diff",
		MIMEType:    "text/x-diff",
	}, r.resourceChangesetDiff)
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "mcpx://remote-sessions/{remote_session_id}/tasks/{task_id}/logs",
		Name:        "终端 Task 日志",
		Description: "读取 MCPX 终端 Task 的完整日志",
		MIMEType:    "text/plain",
	}, r.resourceTaskLogs)
}

func mustSchemaJSON(schema map[string]any) json.RawMessage {
	raw, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}
