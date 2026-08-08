package server

import (
	"encoding/json"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/environment"
	"mcpx/internal/operation"
	"mcpx/internal/server/prompts"
)

// registerTools is the sole public tool registration point.
func (r *Runtime) registerTools(s *mcp.Server) {
	r.registerCleanCoreTools(s)
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
	Title       string
	Meta        mcp.Meta
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
		Title:           annotation.Title,
	}
	if annotation.Title != "" {
		tool.Title = annotation.Title
	}
	if annotation.Meta != nil {
		tool.Meta = annotation.Meta
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
		if branch.Description == "" {
			branch.Description = "仅执行「" + action + "」操作；失败时按返回的 next_action 继续。"
		}
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

// cleanActionTool is the strict action schema used by the final core catalog.
// Every branch repeats common properties so remote models can validate the
// selected action without relying on permissive root-level fallbacks.
func cleanActionTool(name, description string, common map[string]any, branches map[string]actionSchemaBranch, annotation toolAnnotation) mcp.Tool {
	actions := make([]string, 0, len(branches))
	for action := range branches {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	rootProperties := map[string]any{"action": map[string]any{"type": "string", "enum": actions}}
	for key, value := range common {
		rootProperties[key] = value
	}
	oneOf := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		branch := branches[action]
		if branch.Description == "" {
			branch.Description = "仅执行「" + action + "」操作；失败时按返回的 next_action 继续。"
		}
		// JSON Schema evaluates additionalProperties against the object schema
		// where it is declared. Keep every branch field in the root property set
		// as well as in the selected branch; otherwise strict client validators
		// reject valid branch arguments before oneOf is evaluated.
		for key, value := range branch.Properties {
			if _, exists := rootProperties[key]; !exists {
				rootProperties[key] = value
			}
		}
		properties := map[string]any{"action": map[string]any{"const": action}}
		for key, value := range common {
			properties[key] = value
		}
		for key, value := range branch.Properties {
			properties[key] = value
		}
		required := append([]string{}, branch.Required...)
		required = append([]string{"action"}, required...)
		oneOf = append(oneOf, map[string]any{
			"type":                 "object",
			"description":          branch.Description,
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		})
	}
	// Keep the root object open for clients that validate object properties
	// before evaluating oneOf. Each selected branch remains strict and carries
	// the common fields plus its own fields, so the action contract is still
	// enforced by validators that implement oneOf correctly. An open root is
	// required for connectors that flatten or pre-validate discriminated unions.
	raw, _ := json.Marshal(map[string]any{
		"type": "object", "description": description, "properties": rootProperties,
		"required": []string{"action"}, "oneOf": oneOf,
	})
	return annotatedTool(mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(raw)}, annotation)
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func promptToolDescription(descriptions map[string]string, name, fallback string) string {
	if description := descriptions[name]; description != "" {
		return description
	}
	return fallback
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

// registerConsolidatedToolsCatalog registers the tools that are not yet part
// of the P0 clean-core surface. When cleanCore is true, the old workspace,
// session-read, source, and Changeset public names are omitted; their P0
// replacements are registered by registerCleanCoreTools.
func (r *Runtime) registerConsolidatedToolsCatalog(s *mcp.Server, cleanCore bool) {
	toolDesc := prompts.MustDescriptions()
	remoteSession := stringSchema("持久化的 Remote Session 标识")
	workspace := stringSchema("已注册的 Workspace 名称")
	path := stringSchema("工作区相对路径")
	changeItems := arraySchema(changeOperationSchema(), "文件操作列表")
	supportTool := func(name, description string, properties map[string]any, required []string, annotation toolAnnotation) mcp.Tool {
		if cleanCore {
			return cleanCoreTool(name, description, properties, required, annotation)
		}
		return publicTool(name, description, properties, required, annotation)
	}

	if !cleanCore {
		r.addTool(s, publicTool("workspace_read", promptToolDescription(toolDesc, "read", "读取 Workspace 状态与历史"), map[string]any{
			"remote_session_id": remoteSession, "workspace": workspace,
			"view":         enumSchema("读取视图", "list", "changes", "snapshot", "diff", "watch", "memory", "history"),
			"include_diff": booleanSchema("差异视图是否包含 Unified Diff"), "since": stringSchema("由 snapshot 返回的快照 ID"),
			"keyword": stringSchema("记忆或历史关键词"), "id": stringSchema("记忆 ID 或范围"), "time": stringSchema("记忆日期或日期范围"), "latest": numberSchema("最新记忆条数"),
			"session_id": stringSchema("历史过滤用 Session ID"),
			"event_ids":  arraySchema(map[string]any{"type": "string"}, "事件 ID"), "request_ids": arraySchema(map[string]any{"type": "string"}, "请求 ID"),
			"operation_ids": arraySchema(map[string]any{"type": "string"}, "操作 ID"), "task_ids": arraySchema(map[string]any{"type": "string"}, "Task ID"),
			"changeset_ids": arraySchema(map[string]any{"type": "string"}, "Changeset ID"),
			"created_after": stringSchema("RFC3339 或毫秒时间戳"), "created_before": stringSchema("RFC3339 或毫秒时间戳"),
			"kinds": arraySchema(map[string]any{"type": "string"}, "事件类型"), "statuses": arraySchema(map[string]any{"type": "string"}, "事件状态"),
			"limit": numberSchema("返回数量"), "cursor": stringSchema("分页游标"),
		}, []string{"view"}, readOnlyToolAnnotation), r.toolWorkspaceRead)
	}

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
	operationSteps["maxItems"] = operation.MaxSteps
	r.addTool(s, supportTool("operation_batch", toolDesc["operation_batch"], map[string]any{
		"remote_session_id": remoteSession, "operations": operationSteps, "purpose": stringSchema("本次调用的目的；必须由用户明确提供"),
	}, []string{"remote_session_id", "purpose", "operations"}, mutatingToolAnnotation), r.toolOperationBatch)
	operationIDsSchema := arraySchema(map[string]any{"type": "string"}, "批量查询的异步操作 ID；最多 32 个，不要把 operation_manage 嵌套进 operation_batch")
	operationIDsSchema["minItems"] = 1
	operationIDsSchema["maxItems"] = operation.MaxBatchQueries
	operationManage := supportTool("operation_manage", toolDesc["operation_manage"], map[string]any{
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
	if cleanCore {
		operationManageSchema["required"] = []string{"remote_session_id", "action"}
		if properties, ok := operationManageSchema["properties"].(map[string]any); ok {
			if sessionSchema, exists := properties["session_id"]; exists {
				properties["remote_session_id"] = sessionSchema
				delete(properties, "session_id")
			}
		}
	} else {
		operationManageSchema["required"] = []string{"session_id", "action"}
	}
	operationManage.InputSchema = mustSchemaJSON(operationManageSchema)
	r.addTool(s, operationManage, r.toolOperationManage)

	if !cleanCore {
		r.addTool(s, publicTool("session", toolDesc["session"], map[string]any{
			"remote_session_id": remoteSession, "workspace": workspace, "action": enumSchema("生命周期动作", "open", "update", "handoff", "attach", "close"),
			"label": stringSchema("会话标签"), "description": stringSchema("开发目标或新描述"), "client_request_id": stringSchema("客户端幂等键"),
			"include_instructions_content": booleanSchema("返回有界 AGENTS.md 内容"), "include_upstream_tools": booleanSchema("返回上游工具 schema"), "include_project_tasks": booleanSchema("返回项目任务"),
			"known_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "客户端已知版本"},
			"status":          stringSchema("新状态"), "expected_version": numberSchema("乐观锁版本"), "role": stringSchema("接力角色"), "expires_in": numberSchema("接力有效秒数"),
			"note": stringSchema("接力备注"), "handoff_token": stringSchema("一次性接力令牌"), "mode": stringSchema("closed 或 archived"),
		}, []string{"action"}, sessionToolAnnotation), r.toolSession)
		r.addTool(s, publicTool("session_read", promptToolDescription(toolDesc, "session", "读取 Remote Session 状态"), map[string]any{
			"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图", "list", "summary", "events"),
			"query": stringSchema("会话查询条件"), "status": stringSchema("会话状态"), "cursor": stringSchema("分页游标"), "limit": numberSchema("分页数量"), "after_sequence": numberSchema("事件起始序号"),
		}, []string{"view"}, readOnlyToolAnnotation), r.toolSessionRead)
	}

	if !cleanCore {
		r.addTool(s, publicTool("source_read", promptToolDescription(toolDesc, "read", "读取文件与上下文"), map[string]any{
			"remote_session_id": remoteSession, "view": enumSchema("读取视图", "file", "search", "list", "context"), "path": path,
			"mode": enumSchema("文件读取模式；改文件前推荐 mode=full", "window", "full"), "offset": numberSchema("行偏移"), "limit": numberSchema("结果或行数限制"),
			"items": arraySchema(map[string]any{"type": "object", "properties": map[string]any{"path": path, "offset": numberSchema("行偏移"), "limit": numberSchema("行数")}, "required": []string{"path"}}, "批量文件读取项；仅 window"), "max_total_bytes": numberSchema("批量读取总预算"),
			"query": stringSchema("搜索条件"), "search_mode": enumSchema("搜索模式", "smart", "exact", "token"), "parallel": booleanSchema("是否并行召回"),
			"paths": arraySchema(map[string]any{"type": "string"}, "搜索范围"), "include_glob": stringSchema("包含 glob"), "exclude_glob": stringSchema("排除 glob"),
			"cursor": stringSchema("分页游标"), "max_results": numberSchema("最多匹配文件数"), "max_bytes_per_file": numberSchema("单文件预算"),
			"regex": booleanSchema("按 RE2 正则解释"), "case_sensitive": booleanSchema("区分大小写"), "include_sha256": booleanSchema("list/search 是否附带 sha256"),
			"include_instructions": booleanSchema("返回适用指令"), "context_before": numberSchema("匹配前上下文行数"), "context_after": numberSchema("匹配后上下文行数"),
		}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolSourceRead)
	}

	if !cleanCore {
		r.addTool(s, publicTool("change", promptToolDescription(toolDesc, "edit", "执行文件变更"), map[string]any{
			"remote_session_id":  remoteSession,
			"action":             enumSchema("变更动作", "prepare", "discard", "apply", "revert"),
			"summary":            stringSchema("变更摘要"),
			"operations":         changeItems,
			"idempotency_key":    stringSchema("业务幂等键"),
			"apply":              booleanSchema("prepare 时是否立即应用；默认 false"),
			"format":             booleanSchema("格式化变更文件"),
			"verify":             arraySchema(map[string]any{"type": "string"}, "验证步骤"),
			"purpose":            stringSchema("本次调用的目的；必须由用户明确提供"),
			"changeset_id":       stringSchema("Changeset ID；apply/discard/revert 必填；须原样复制返回值"),
			"expected_digest":    stringSchema("apply 必填；必须原样复制 prepare/read 返回的 digest；禁止填 diff 统计、tree_digest、snapshot ID 或空值"),
			"confirmation_token": stringSchema("仅表示用户已确认同一变更，不是认证凭据"),
		}, []string{"remote_session_id", "action", "purpose"}, mutatingToolAnnotation), r.toolChange)
		r.addTool(s, publicTool("change_read", promptToolDescription(toolDesc, "observe", "查看文件变更历史"), map[string]any{
			"remote_session_id": remoteSession, "view": enumSchema("读取视图", "diff", "history"), "changeset_id": stringSchema("Changeset ID"),
			"limit": numberSchema("历史数量"), "known_history_digest": stringSchema("上一次 history 返回的 history_digest"),
		}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolChangeRead)
	}

	if !cleanCore {
		r.addTool(s, publicTool("command_run", promptToolDescription(toolDesc, "execute", "按策略执行命令"), map[string]any{
			"remote_session_id": remoteSession, "command": stringSchema("shell 命令"), "task": stringSchema("项目任务名称"),
			"purpose": stringSchema("执行原因"), "scope": enumSchema("执行范围", "workspace"),
			"confirmation_token": stringSchema("仅表示用户已确认同一命令"), "yield_time_ms": numberSchema("等待时长"),
		}, []string{"remote_session_id", "purpose"}, commandExecutionToolAnnotation), r.toolCommandRun)
		r.addTool(s, publicTool("task_read", promptToolDescription(toolDesc, "observe", "查看 Task 状态与日志"), map[string]any{
			"remote_session_id": remoteSession, "view": enumSchema("读取视图", "list", "status", "logs", "ports", "diagnostics"),
			"task_id": stringSchema("Task ID"), "known_task_digest": stringSchema("task_list_digest"), "stdout_offset": numberSchema("stdout 偏移"),
			"stderr_offset": numberSchema("stderr 偏移"), "limit": numberSchema("数量限制"), "yield_time_ms": numberSchema("等待时长"),
		}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolTaskRead)
		r.addTool(s, publicTool("task", promptToolDescription(toolDesc, "execute", "控制 Task 生命周期"), map[string]any{
			"remote_session_id": remoteSession, "action": enumSchema("控制动作", "attach", "stop", "stdin"), "task_id": stringSchema("Task ID"),
			"stdout_offset": numberSchema("stdout 偏移"), "stderr_offset": numberSchema("stderr 偏移"), "yield_time_ms": numberSchema("等待时长"),
			"force": booleanSchema("强制终止"), "input": stringSchema("stdin 文本"), "confirmation_token": stringSchema("语义确认"), "purpose": stringSchema("目的"),
		}, []string{"remote_session_id", "action", "task_id", "purpose"}, mutatingToolAnnotation), r.toolTask)

		r.addTool(s, publicTool("plan", toolDesc["plan"], map[string]any{
			"remote_session_id": remoteSession, "action": enumSchema("计划动作", "create", "start_task", "complete_task", "block_task", "replan", "deliver"),
			"goal": stringSchema("计划目标"), "summary": stringSchema("计划摘要"), "tasks": arraySchema(planTaskInputSchema(), "有序计划任务"),
			"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("计划任务 ID"), "evidence": arraySchema(planEvidenceSchema(), "证据"),
			"reason": stringSchema("阻塞或重新规划原因"), "operations": arraySchema(planOperationSchema(), "计划任务操作"), "purpose": stringSchema("目的"),
		}, []string{"remote_session_id", "action", "purpose"}, mutatingToolAnnotation), r.toolPlan)
		r.addTool(s, publicTool("plan_read", promptToolDescription(toolDesc, "plan", "读取持久化计划"), map[string]any{
			"remote_session_id": remoteSession, "plan_id": stringSchema("Plan ID"),
		}, []string{"remote_session_id", "plan_id"}, readOnlyToolAnnotation), r.toolPlanReadPublic)

		r.addTool(s, publicTool("extension_discover", promptToolDescription(toolDesc, "discover", "发现 Skill 与上游 MCP"), map[string]any{
			"workspace": workspace, "remote_session_id": remoteSession, "kind": enumSchema("扩展类型", "skill", "mcp"),
			"view": enumSchema("发现视图", "list", "describe"), "query": stringSchema("关键词"), "name": stringSchema("Skill 或 MCP 名称"),
			"server": stringSchema("MCP Server 名称"), "include_tools": booleanSchema("返回上游工具 schema"),
		}, []string{"kind", "view"}, readOnlyToolAnnotation), r.toolExtensionDiscover)
		r.addTool(s, publicTool("skill_call", toolDesc["skill_call"], map[string]any{
			"remote_session_id": remoteSession, "name": stringSchema("Skill 名称"),
			"arguments": map[string]any{"type": "object", "additionalProperties": true}, "confirmation_token": stringSchema("语义确认"), "purpose": stringSchema("目的"),
		}, []string{"remote_session_id", "name", "purpose"}, commandExecutionToolAnnotation), r.toolSkillCall)
		r.addTool(s, publicTool("mcp_call", toolDesc["mcp_call"], map[string]any{
			"remote_session_id": remoteSession, "server": stringSchema("MCP Server 名称"), "tool": stringSchema("上游工具名称"),
			"arguments": map[string]any{"type": "object", "additionalProperties": true}, "confirmation_token": stringSchema("语义确认"), "purpose": stringSchema("目的"),
		}, []string{"remote_session_id", "server", "tool", "purpose"}, commandExecutionToolAnnotation), r.toolMCPCallPublic)

		r.addTool(s, publicTool("artifact_read", promptToolDescription(toolDesc, "artifact", "读取已登记产物"), map[string]any{
			"remote_session_id": remoteSession, "view": enumSchema("读取视图", "list", "content"), "kind": stringSchema("产物类型"),
			"artifact_id": stringSchema("产物 ID"), "offset": numberSchema("字节偏移"), "limit": numberSchema("字节数量"),
		}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolArtifactReadPublic)
		r.addTool(s, publicTool("artifact", toolDesc["artifact"], map[string]any{
			"remote_session_id": remoteSession, "path": path, "name": stringSchema("显示名称"), "kind": stringSchema("产物类型"), "mime_type": stringSchema("MIME 类型"),
		}, []string{"remote_session_id", "path"}, mutatingToolAnnotation), r.toolArtifact)
	}

	r.addTool(s, supportTool("runtime_read", toolDesc["runtime_read"], map[string]any{
		"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图", "capabilities", "project", "instructions"),
		"include_tool_schemas": booleanSchema("返回工具 schema"), "include_skill_details": booleanSchema("返回 Skill 详情"),
		"known_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"anchor_path":     stringSchema("指令锚点路径"), "paths": arraySchema(map[string]any{"type": "string"}, "指令路径"),
	}, []string{"view"}, readOnlyToolAnnotation), r.toolRuntimeRead)
	r.addTool(s, supportTool("environment_read", toolDesc["environment_read"], map[string]any{
		"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图", "current", "compare"),
		"sections": arraySchema(map[string]any{"type": "string", "enum": environment.ValidSections}, "环境分区"), "snapshot_id": stringSchema("比较用快照 ID"),
	}, []string{"view"}, readOnlyToolAnnotation), r.toolEnvironmentRead)
	r.addTool(s, supportTool("environment", toolDesc["environment"], map[string]any{
		"remote_session_id": remoteSession, "action": enumSchema("环境写动作", "snapshot_create"),
		"sections": arraySchema(map[string]any{"type": "string", "enum": environment.ValidSections}, "环境分区"),
	}, []string{"remote_session_id", "action"}, sessionToolAnnotation), r.toolEnvironment)

	if cleanCore {
		executeCommon := map[string]any{
			"remote_session_id": remoteSession, "purpose": stringSchema("本次执行的用户目标"),
			"idempotency_key": stringSchema("同一执行请求重试时复用的幂等键"),
			"scope":           enumSchema("执行范围", "workspace"), "yield_time_ms": numberSchema("等待时长"),
			"user_confirmed": booleanSchema("用户已确认同一命令；服务端仍会校验待确认摘要"),
			"execution_mode": enumSchema("async 只表示 Operation 异步调度；是否返回 Task ID 由命令是否超过 yield_time_ms 决定", "sync", "async"),
		}
		executeBranches := map[string]actionSchemaBranch{
			"run": {Description: "执行 command 或项目 task；短命令同步返回，长命令返回 task_id。", Properties: map[string]any{
				"command": stringSchema("Workspace 内待执行的简单命令"), "task": stringSchema("项目任务名称，与 command 二选一"),
			}, Required: []string{"remote_session_id", "purpose"}},
			"attach": {Description: "等待并读取已有 Task 的输出。", Properties: map[string]any{
				"task_id": stringSchema("服务端返回的 Task ID"), "stdout_offset": numberSchema("stdout 字节偏移"),
				"stderr_offset": numberSchema("stderr 字节偏移"),
			}, Required: []string{"remote_session_id", "purpose", "task_id"}},
			"stop": {Description: "停止属于当前 Remote Session 的 Task。", Properties: map[string]any{
				"task_id": stringSchema("服务端返回的 Task ID"),
			}, Required: []string{"remote_session_id", "purpose", "task_id"}},
			"stdin": {Description: "向交互式 Task 写入 stdin。", Properties: map[string]any{
				"task_id": stringSchema("服务端返回的 Task ID"), "input": stringSchema("写入 stdin 的文本"),
			}, Required: []string{"remote_session_id", "purpose", "task_id", "input"}},
		}
		r.addTool(s, cleanActionTool("execute", toolDesc["execute"], executeCommon, executeBranches, commandExecutionToolAnnotation), r.toolExecute)

		planCommon := map[string]any{
			"remote_session_id": remoteSession, "purpose": stringSchema("本次计划操作的用户目标"),
			"idempotency_key": stringSchema("同一计划写操作重试时复用的幂等键"), "execution_mode": enumSchema("执行模式", "sync", "async"),
		}
		planBranches := map[string]actionSchemaBranch{
			"create":   {Properties: map[string]any{"goal": stringSchema("计划目标"), "summary": stringSchema("计划摘要"), "tasks": arraySchema(planTaskInputSchema(), "有序计划任务")}, Required: []string{"remote_session_id", "purpose", "goal", "tasks"}},
			"read":     {Properties: map[string]any{"plan_id": stringSchema("服务端返回的 Plan ID")}, Required: []string{"remote_session_id", "plan_id"}},
			"advance":  {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("服务端返回的正式 Task ID")}, Required: []string{"remote_session_id", "purpose", "plan_id", "task_id"}},
			"complete": {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("服务端返回的正式 Task ID"), "evidence": arraySchema(planEvidenceSchema(), "完成任务所需证据")}, Required: []string{"remote_session_id", "purpose", "plan_id", "task_id", "evidence"}},
			"block":    {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("服务端返回的正式 Task ID"), "reason": stringSchema("阻塞原因"), "evidence": arraySchema(planEvidenceSchema(), "已获得证据")}, Required: []string{"remote_session_id", "purpose", "plan_id", "task_id", "reason"}},
			"replan":   {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "goal": stringSchema("新的计划目标"), "summary": stringSchema("新的计划摘要"), "reason": stringSchema("重新规划原因"), "operations": arraySchema(planOperationSchema(), "新增、更新或移除任务")}, Required: []string{"remote_session_id", "purpose", "plan_id", "reason", "operations"}},
			"deliver":  {Properties: map[string]any{"plan_id": stringSchema("Plan ID")}, Required: []string{"remote_session_id", "purpose", "plan_id"}},
		}
		r.addTool(s, cleanActionTool("plan", toolDesc["plan"], planCommon, planBranches, mutatingToolAnnotation), r.toolPlanClean)

		artifactCommon := map[string]any{"remote_session_id": remoteSession, "purpose": stringSchema("本次产物操作的用户目标"), "idempotency_key": stringSchema("同一登记操作重试时复用的幂等键"), "execution_mode": enumSchema("执行模式", "sync", "async")}
		artifactBranches := map[string]actionSchemaBranch{
			"register": {Properties: map[string]any{"path": path, "name": stringSchema("显示名称"), "kind": enumSchema("产物类型", "test_report", "coverage", "build", "screenshot", "log", "other"), "mime_type": stringSchema("MIME 类型")}, Required: []string{"remote_session_id", "purpose", "path"}},
			"list":     {Properties: map[string]any{"kind": stringSchema("按产物类型过滤"), "limit": numberSchema("返回数量")}, Required: []string{"remote_session_id"}},
			"read":     {Properties: map[string]any{"artifact_id": stringSchema("服务端返回的 Artifact ID"), "offset": numberSchema("字节偏移"), "limit": numberSchema("字节数量")}, Required: []string{"remote_session_id", "artifact_id"}},
		}
		r.addTool(s, cleanActionTool("artifact", toolDesc["artifact"], artifactCommon, artifactBranches, mutatingToolAnnotation), r.toolArtifactClean)

		discoverCommon := map[string]any{"remote_session_id": remoteSession, "kind": enumSchema("发现对象类型", "skill", "mcp"), "view": enumSchema("发现视图", "list", "describe"), "query": stringSchema("关键词"), "name": stringSchema("Skill 或 MCP 名称"), "server": stringSchema("MCP Server 名称"), "include_tools": booleanSchema("返回 MCP 上游工具 schema"), "execution_mode": enumSchema("执行模式", "sync", "async")}
		r.addTool(s, cleanCoreTool("discover", toolDesc["discover"], discoverCommon, []string{"remote_session_id", "kind", "view"}, readOnlyToolAnnotation), r.toolDiscover)
		skillCommon := map[string]any{"remote_session_id": remoteSession, "purpose": stringSchema("调用 Skill 的用户目标"), "name": stringSchema("必须来自 discover 的 Skill 名称"), "arguments": map[string]any{"type": "object", "additionalProperties": true}, "discovery_id": stringSchema("discover 返回的 discovery_id"), "discovery_revision": stringSchema("discover 返回的 discovery_revision"), "user_confirmed": booleanSchema("用户已确认同一 Skill 调用"), "idempotency_key": stringSchema("同一调用重试时复用的幂等键"), "execution_mode": enumSchema("执行模式", "sync", "async")}
		r.addTool(s, cleanCoreTool("skill_call", toolDesc["skill_call"], skillCommon, []string{"remote_session_id", "purpose", "name", "discovery_id", "discovery_revision"}, commandExecutionToolAnnotation), r.toolSkillCallClean)
		mcpCommon := map[string]any{"remote_session_id": remoteSession, "purpose": stringSchema("调用上游 MCP 的用户目标"), "server": stringSchema("必须来自 discover 的 MCP Server"), "tool": stringSchema("必须来自 discover 的上游工具"), "arguments": map[string]any{"type": "object", "additionalProperties": true}, "discovery_id": stringSchema("discover 返回的 discovery_id"), "discovery_revision": stringSchema("discover 返回的 discovery_revision"), "user_confirmed": booleanSchema("用户已确认同一 MCP 调用"), "idempotency_key": stringSchema("同一调用重试时复用的幂等键"), "execution_mode": enumSchema("执行模式", "sync", "async")}
		r.addTool(s, cleanCoreTool("mcp_call", toolDesc["mcp_call"], mcpCommon, []string{"remote_session_id", "purpose", "server", "tool", "discovery_id", "discovery_revision"}, commandExecutionToolAnnotation), r.toolMCPCallClean)
	}

	r.addTool(s, supportTool("screenshot_capture", toolDesc["screenshot_capture"], map[string]any{
		"remote_session_id": remoteSession, "mode": stringSchema("全屏或区域"), "display": numberSchema("显示器索引"),
		"x": numberSchema("区域 X"), "y": numberSchema("区域 Y"), "width": numberSchema("宽度"), "height": numberSchema("高度"),
		"compression": stringSchema("压缩模式"), "format": stringSchema("png 或 jpeg"), "quality": numberSchema("JPEG 质量"),
		"max_width": numberSchema("输出宽度上限"), "max_height": numberSchema("输出高度上限"),
	}, []string{"remote_session_id"}, readOnlyToolAnnotation), r.toolScreenshotCapture)
	r.addTool(s, supportTool("secret_provide", toolDesc["secret_provide"], map[string]any{
		"remote_session_id": remoteSession, "secret_id": stringSchema("Secret ID"),
		"values": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Secret 名称和值"},
	}, []string{"remote_session_id"}, secretToolAnnotation), r.toolSecretsProvide)

	r.registerResources(s)
}

func (r *Runtime) registerResources(s *mcp.Server) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}",
		Name:        "Remote Session 产物",
		Description: "读取已注册的 MCPX 开发产物",
	}, r.resourceArtifact)
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
