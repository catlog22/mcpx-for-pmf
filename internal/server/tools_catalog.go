package server

import (
	"encoding/json"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/environment"
	"mcpx/internal/operation"
)

// registerTools is the sole public tool registration point. The legacy
// fine-grained handlers remain private implementation details behind the
// explicit 30-tool catalog below.
func (r *Runtime) registerTools(s *mcpserver.MCPServer) {
	r.registerConsolidatedToolsV2(s)
	r.captureToolIndex(s)
}

func (r *Runtime) captureToolIndex(s *mcpserver.MCPServer) {
	listed := s.ListTools()
	index := make(map[string]mcp.Tool, len(listed))
	for name, registered := range listed {
		index[name] = registered.Tool
	}
	r.toolIndexMu.Lock()
	r.toolIndex = index
	r.toolIndexMu.Unlock()
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

// compactToolResult keeps the unstructured content concise. The public ARC
// wrapper moves the complete machine-readable result to response metadata.
func compactToolResult(data any, summary string) *mcp.CallToolResult {
	return mcp.NewToolResultStructured(data, summary)
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
	tool.Annotations.ReadOnlyHint = mcp.ToBoolPtr(annotation.ReadOnly)
	tool.Annotations.DestructiveHint = mcp.ToBoolPtr(annotation.Destructive)
	tool.Annotations.IdempotentHint = mcp.ToBoolPtr(annotation.Idempotent)
	tool.Annotations.OpenWorldHint = mcp.ToBoolPtr(annotation.OpenWorld)
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
	tool := annotatedTool(mcp.NewTool(name, mcp.WithDescription(description)), annotation)
	tool.InputSchema = mcp.ToolInputSchema{}
	tool.RawInputSchema = raw
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
	tool := annotatedTool(mcp.NewTool(name, mcp.WithDescription(description)), annotation)
	tool.InputSchema = mcp.ToolInputSchema{}
	tool.RawInputSchema = raw
	return tool
}

func changeOperationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation":   map[string]any{"type": "string", "enum": []string{"update", "create", "rename", "delete", "replace_exact", "insert_before", "insert_after", "delete_exact", "replace_range"}, "description": "操作类型：create 创建文件（缺失的父目录会自动创建，不能把目录路径作为普通文件创建）；update 只应用基于当前版本的局部 unified diff，并保留目标文件原有换行格式，避免把局部修改变成整文件换行变更；rename 移动到 new_path；delete 删除文件或目录（目录会先原子移入变更集回收区，避免枚举目录内文件）；replace_exact 替换精确文本；insert_before/insert_after 在匹配点前后插入；delete_exact 删除精确文本；replace_range 重写行范围。"},
			"path":        map[string]any{"type": "string", "description": "工作区相对路径；rename 时表示源路径。replace_exact/insert_before/insert_after/delete_exact/replace_range/update 可对同一 path 提交多个操作按顺序依次应用；create/delete/rename 每个 path 只能出现一次"},
			"new_path":    map[string]any{"type": "string", "description": "rename 的目标路径；仅 operation=rename 使用"},
			"base_sha256": map[string]any{"type": "string", "description": "source_read 返回的当前文件版本；create 可省略，delete 也可省略（服务端会在准备时捕获并在应用前复核）"},
			"patch":       map[string]any{"type": "string", "description": "operation=update 使用的局部 unified diff；必须基于当前 base_sha256，并保留目标文件原有换行格式。若无法确认 hunk 局部且上下文一致，优先使用 replace_exact/insert_*"},
			"content":     map[string]any{"type": "string", "description": "operation=create 的完整文件内容；每次最多新增 300 行，大文件用 insert_after 分段追加；不要写入指令文本"},
			"match":       map[string]any{"type": "string", "description": "replace_exact、insert_before、insert_after 或 delete_exact 的匹配文本；按 source_read 返回的 line_ending 保留目标文件原有换行格式"},
			"replacement": map[string]any{"type": "string", "description": "replace_exact 或 replace_range 的替换文本"},
			"occurrence":  map[string]any{"type": "string", "enum": []string{"one"}, "description": "仅替换第一个匹配项（默认）"},
			"range_start": map[string]any{"type": "number", "description": "replace_range 的起始行号，从 1 开始"},
			"range_end":   map[string]any{"type": "number", "description": "replace_range 的结束行号（包含此行）"},
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

func (r *Runtime) registerConsolidatedTools(s *mcpserver.MCPServer) {
	remoteSession := mcp.WithString("remote_session_id", mcp.Description("持久化的 Remote Session 标识"))
	commandRemoteSession := mcp.WithString("remote_session_id", mcp.Required(), mcp.Description("持久化的 Remote Session 标识"))
	workspace := mcp.WithString("workspace", mcp.Description("已注册的 Workspace 名称"))
	operationItems := mcp.Items(changeOperationSchema())

	// High-frequency development tools.
	r.addTool(s, annotatedTool(mcp.NewTool("workspace_list", mcp.WithDescription("列出已注册的 Workspace，供模型在 session_open 前选择有效工作区。未知工作区时先调用；本工具不会创建、修改或读取文件。成功后将返回的 workspace 名称传给 session_open。")), readOnlyToolAnnotation), r.toolWorkspaceList)
	r.addTool(s, annotatedTool(mcp.NewTool("session_open",
		mcp.WithDescription("创建或恢复 Remote Session，并返回开发所需的启动上下文。先调用 workspace_list，或提供已知的 remote_session_id；成功后向用户展示完整的 remote_session_id 及其绑定的 Workspace。本工具不会读取、修改文件或执行命令。工作区不存在时，重新调用 workspace_list 并使用准确名称重试。"),
		remoteSession, workspace,
		mcp.WithString("label", mcp.Description("会话标签")),
		mcp.WithString("description", mcp.Description("开发目标")),
		mcp.WithString("client_request_id", mcp.Description("客户端幂等键")),
		mcp.WithBoolean("include_instructions_content", mcp.Description("返回有界的 AGENTS.md 内容")),
		mcp.WithBoolean("include_upstream_tools", mcp.Description("发现上游工具 schema")),
		mcp.WithBoolean("include_project_tasks", mcp.Description("返回已发现的项目任务")),
		mcp.WithObject("known_revisions", mcp.Description("客户端已知的版本；未变化的能力数据不会重复返回"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
	), sessionToolAnnotation), r.toolSessionOpen)
	r.addTool(s, annotatedTool(mcp.NewTool("file_read",
		mcp.WithDescription("读取一个或多个工作区相对路径的文件窗口，也支持非源码文件。模型或客户端需要完整 HTML、图片或其他文件时，对单个 path 使用 mode=full；图片以直接 MCP ImageContent 返回，不使用 Resource URI。生成 change_execute 请求前或版本冲突后使用；本工具不执行命令、不修改文件。路径不存在时，按返回的 context_query 恢复动作处理。"), remoteSession,
		mcp.WithString("path", mcp.Description("单个读取目标的工作区相对路径")),
		mcp.WithString("mode", mcp.Enum("window", "full"), mcp.Description("window：默认的有界读取；full：完整返回单个文件供客户端预览")),
		mcp.WithNumber("offset", mcp.Description("从 0 开始的行偏移")),
		mcp.WithNumber("limit", mcp.Description("读取行数")),
		mcp.WithArray("items", mcp.Description("批量读取项：{path, offset, limit}"), mcp.Items(map[string]any{
			"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "number"}, "limit": map[string]any{"type": "number"},
			}, "required": []string{"path"},
		}), mcp.MinItems(1)),
		mcp.WithNumber("max_total_bytes", mcp.Description("批量读取的总内容预算")),
	), readOnlyToolAnnotation), r.toolFileReadUnified)
	r.addTool(s, annotatedTool(mcp.NewTool("context_query",
		mcp.WithDescription("在 Workspace 内搜索、列举并组装有界源码上下文；由 action 选择操作。list 仅返回普通文件，不能证明目录完整性或目录存在；不要根据匹配路径推断顶层目录清单。路径未知时用于定位文件或符号；本工具不执行命令、不修改文件。结果被截断时，按返回的下一步继续调用 context_query。"), remoteSession,
		mcp.WithString("action", mcp.Required(), mcp.Enum("query", "search", "list")),
		mcp.WithString("query", mcp.Description("用户搜索条件")),
		mcp.WithString("mode", mcp.Enum("smart", "exact", "token"), mcp.Description("搜索模式；默认 smart")),
		mcp.WithBoolean("parallel", mcp.Description("并行执行 exact 和 token 召回；默认 true")),
		mcp.WithNumber("max_results", mcp.Description("最多返回的排序文件数；默认 20")),
		mcp.WithArray("paths", mcp.Description("起始路径或目录范围"), mcp.WithStringItems()),
		mcp.WithString("include_glob", mcp.Description("可选的包含 glob")),
		mcp.WithString("exclude_glob", mcp.Description("可选的排除 glob")),
		mcp.WithString("cursor", mcp.Description("分页游标")),
		mcp.WithNumber("limit", mcp.Description("结果数量限制")),
		mcp.WithNumber("max_bytes_per_file", mcp.Description("单文件读取预算")),
		mcp.WithBoolean("regex", mcp.Description("将搜索条件按 RE2 正则解释")),
		mcp.WithBoolean("case_sensitive", mcp.Description("字面搜索是否区分大小写")),
		mcp.WithBoolean("include_sha256", mcp.Description("返回匹配文件的版本哈希")),
		mcp.WithBoolean("include_instructions", mcp.Description("返回适用的 AGENTS 元数据")),
		mcp.WithNumber("context_before", mcp.Description("每个匹配前的上下文行数")),
		mcp.WithNumber("context_after", mcp.Description("每个匹配后的上下文行数")),
	), readOnlyToolAnnotation), r.toolContextQueryUnified)
	changeExecute := mcp.NewTool("change_execute",
		mcp.WithDescription("唯一的 MCPX 文件修改入口：校验文件操作、当前版本和安全策略，然后预览或原子应用。先使用 file_read/context_query 获取上下文；本工具不运行测试、不检查 Git。用户原始请求已明确授权时可在 operations 首次提交 user_confirmed=true；否则先展示摘要，确认后使用原 Changeset 参数并设置 user_confirmed=true 重试。删除与创建不能放在同一次调用，必须先删除并确认成功、重新读取工作区，再创建。"), remoteSession,
		mcp.WithString("summary", mcp.Description("变更摘要")),
		mcp.WithArray("operations", mcp.Description("新建变更时必填的文件操作"), operationItems, mcp.MinItems(1)),
		mcp.WithString("changeset_id", mcp.Description("通过本入口应用的已准备 Changeset 草稿 ID")),
		mcp.WithString("expected_digest", mcp.Description("必须原样复制准备 Changeset 返回的 digest；不要填写 diff 统计、tree_digest、snapshot ID 或空值")),
		mcp.WithString("revert_changeset_id", mcp.Description("通过本入口回滚的已应用 Changeset ID")),
		mcp.WithString("idempotency_key", mcp.Description("重试同一变更时使用的业务幂等键")),
		mcp.WithBoolean("user_confirmed", mcp.Description("用户已在对话中明确确认本次变更；原始请求已明确授权时可随 operations 首次提交，否则仅在原 changeset_id 和 expected_digest 重试时设置为 true")),
		mcp.WithBoolean("apply", mcp.Description("准备后立即应用；默认 true")),
		mcp.WithBoolean("format", mcp.Description("格式化已变更文件")),
		mcp.WithArray("verify", mcp.Description("验证步骤：format、typecheck、lint 或 related_tests"), mcp.WithStringItems()),
	)
	changeExecute.InputSchema = mcp.ToolInputSchema{}
	changeExecute.RawInputSchema, _ = json.Marshal(changeExecuteInputSchema())
	r.addTool(s, annotatedTool(changeExecute, mutatingToolAnnotation), r.toolChangeExecute)
	r.addTool(s, annotatedTool(mcp.NewTool("command_execute",
		mcp.WithDescription("在选定 Workspace 中执行用户明确要求的测试、构建、格式化或其他开发命令。命令会经过策略检查、审计并支持取消，需要确认时向用户展示命令和用途，确认后用相同 command、purpose 并设置 user_confirmed=true 重试；运行中的命令使用 task_manage 管理。本工具不读取文件、不检查 Git 状态、不应用补丁。"), commandRemoteSession,
		mcp.WithString("command", mcp.Description("用户要求执行的 shell 命令")),
		mcp.WithString("task", mcp.Description("已发现的项目任务名称")),
		mcp.WithString("purpose", mcp.Required(), mcp.Description("用户要求执行此命令的原因；不要自行编造")),
		mcp.WithString("scope", mcp.Enum("workspace"), mcp.Description("执行范围；默认 workspace")),
		mcp.WithBoolean("user_confirmed", mcp.Description("用户已在对话中明确确认本次命令；仅在相同 command 和 purpose 重试时设置为 true")),
		mcp.WithNumber("yield_time_ms", mcp.Description("等待时长；默认 10000，最大 60000")),
	), commandExecutionToolAnnotation), r.toolCommandExecute)
	r.addTool(s, annotatedTool(mcp.NewTool("progress_report",
		mcp.WithDescription("当工具调用后将暂停、结束、等待用户或暂时不再调用工具时，记录简洁且用户可见的进度。填写已验证结果和下一步；不要写入隐藏思维链。"), remoteSession,
		mcp.WithString("summary", mcp.Required(), mcp.Description("已验证的进度摘要")),
		mcp.WithString("result_summary", mcp.Description("上一次工具调用的简短结果")),
		mcp.WithString("status", mcp.Enum("in_progress", "completed", "waiting_for_user", "blocked"), mcp.Description("当前进度状态")),
		mcp.WithString("next_step", mcp.Description("下一步操作或需要用户回答的问题")),
		mcp.WithString("related_tool", mcp.Description("关联的上一工具")),
	), readOnlyToolAnnotation), r.toolProgressReport)

	// Domain management tools. Each action has an explicit JSON Schema branch.
	operationArraySchema := map[string]any{"type": "array", "items": changeOperationSchema(), "minItems": 1}
	r.addTool(s, actionTool("session_manage", "管理 session_open 之后的 Remote Session 生命周期，包括列出、查看、接力、更新和关闭。本工具不选择工作区、不读取或修改文件、不执行命令。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"), "workspace": stringSchema("已注册的 Workspace 名称"),
	}, map[string]actionSchemaBranch{
		"list":    {Properties: map[string]any{"query": stringSchema("会话查询条件"), "status": stringSchema("会话状态"), "cursor": stringSchema("分页游标"), "limit": numberSchema("分页数量")}},
		"get":     {},
		"events":  {Properties: map[string]any{"after_sequence": numberSchema("起始事件序号"), "limit": numberSchema("分页数量")}},
		"update":  {Properties: map[string]any{"label": stringSchema("新会话标签"), "description": stringSchema("新会话描述"), "status": stringSchema("会话状态"), "expected_version": numberSchema("乐观锁版本")}},
		"handoff": {Properties: map[string]any{"role": stringSchema("接力角色"), "expires_in": numberSchema("接力有效秒数"), "note": stringSchema("接力备注")}, Required: []string{"role"}},
		"attach":  {Properties: map[string]any{"handoff_token": stringSchema("一次性接力令牌")}, Required: []string{"handoff_token"}},
		"close":   {Properties: map[string]any{"mode": stringSchema("closed 或 archived")}},
	}), r.toolSessionManage)
	r.addTool(s, actionToolWithAnnotation("change_manage", "准备或查看 Changeset，不直接修改工作区文件。使用 diff 和 history 审阅；应用或回滚统一使用 change_execute，不要用 command_execute 处理补丁。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"),
	}, map[string]actionSchemaBranch{
		"prepare": {Properties: map[string]any{"summary": stringSchema("变更摘要"), "operations": operationArraySchema}, Required: []string{"operations"}},
		"diff":    {Properties: map[string]any{"changeset_id": stringSchema("Changeset ID")}, Required: []string{"changeset_id"}},
		"history": {Properties: map[string]any{"limit": numberSchema("历史记录数量")}},
	}, sessionToolAnnotation), r.toolChangeManage)
	r.addTool(s, actionTool("task_manage", "管理 command_execute 启动的运行中命令和项目 Task。使用准确的 remote_session_id 与 task_id 执行 attach、status、logs、stop 或 stdin；本工具不启动新命令、不修改文件。Task ID 不存在时先 list，不要重试旧 ID。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"),
	}, map[string]actionSchemaBranch{
		"attach":      {Properties: map[string]any{"task_id": stringSchema("Task ID"), "stdout_offset": numberSchema("stdout 绝对字节偏移"), "stderr_offset": numberSchema("stderr 绝对字节偏移"), "yield_time_ms": numberSchema("attach 等待时长")}, Required: []string{"task_id"}},
		"status":      {Properties: map[string]any{"task_id": stringSchema("Task ID")}, Required: []string{"task_id"}},
		"logs":        {Properties: map[string]any{"task_id": stringSchema("Task ID"), "stdout_offset": numberSchema("stdout 绝对字节偏移"), "stderr_offset": numberSchema("stderr 绝对字节偏移")}, Required: []string{"task_id"}},
		"list":        {Properties: map[string]any{"limit": numberSchema("Task 数量")}},
		"stop":        {Properties: map[string]any{"task_id": stringSchema("Task ID"), "force": booleanSchema("强制终止 Task")}, Required: []string{"task_id"}},
		"ports":       {Properties: map[string]any{"task_id": stringSchema("Task ID")}, Required: []string{"task_id"}},
		"diagnostics": {Properties: map[string]any{"task_id": stringSchema("Task ID"), "limit": numberSchema("诊断信息数量")}, Required: []string{"task_id"}},
		"stdin":       {Properties: map[string]any{"task_id": stringSchema("Task ID"), "input": stringSchema("stdin 文本")}, Required: []string{"task_id", "input"}},
	}), r.toolTaskManage)
	r.addTool(s, actionTool("plan_manage", "创建和推进持久化开发 Plan。create/get 返回精确 plan_id 与每项 task_id；后续操作只能使用这些返回 ID，缺失时先 get，不要猜测。仅用于管理计划状态和证据；本工具不修改文件、不运行 Task，实际执行分别使用 change_execute 和 command_execute。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"),
	}, map[string]actionSchemaBranch{
		"create":        {Properties: map[string]any{"goal": stringSchema("计划目标"), "summary": stringSchema("计划摘要"), "tasks": arraySchema(planTaskInputSchema(), "有序的计划任务；每项应提供语义稳定 task_id")}, Required: []string{"goal", "tasks"}},
		"get":           {Properties: map[string]any{"plan_id": stringSchema("Plan ID")}, Required: []string{"plan_id"}},
		"start_task":    {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("Plan Task ID")}, Required: []string{"plan_id", "task_id"}},
		"complete_task": {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("Plan Task ID"), "evidence": arraySchema(planEvidenceSchema(), "完成证据")}, Required: []string{"plan_id", "task_id", "evidence"}},
		"block_task":    {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "task_id": stringSchema("Plan Task ID"), "reason": stringSchema("阻塞原因"), "evidence": arraySchema(planEvidenceSchema(), "阻塞证据")}, Required: []string{"plan_id", "task_id", "reason"}},
		"replan":        {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "goal": stringSchema("更新后的计划目标"), "summary": stringSchema("更新后的计划摘要"), "reason": stringSchema("重新规划原因"), "operations": arraySchema(planOperationSchema(), "计划任务操作")}, Required: []string{"plan_id", "reason", "operations"}},
		"deliver":       {Properties: map[string]any{"plan_id": stringSchema("Plan ID")}, Required: []string{"plan_id"}},
	}), r.toolPlanManage)
	r.addTool(s, annotatedTool(actionTool("runtime_inspect", "查看 MCPX 能力、项目摘要或指定范围的 AGENTS 指令。选工具前或能力版本变化后使用；本工具不执行命令、不修改文件。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"), "workspace": stringSchema("已注册的 Workspace 名称"),
	}, map[string]actionSchemaBranch{
		"capabilities": {Properties: map[string]any{"include_tool_schemas": booleanSchema("返回完整的已注册工具 schema"), "include_skill_details": booleanSchema("返回完整的 Skill 元数据"), "known_revisions": map[string]any{"type": "object", "description": "客户端之前观察到的能力版本", "additionalProperties": map[string]any{"type": "string"}}}},
		"project":      {},
		"instructions": {Properties: map[string]any{"anchor_path": stringSchema("工作区相对的指令锚点路径"), "paths": arraySchema(map[string]any{"type": "string"}, "多路径指令解析")}},
	}), readOnlyToolAnnotation), r.toolRuntimeInspect)
	r.addTool(s, annotatedTool(mcp.NewTool("environment_inspect", mcp.WithDescription("查看选定 Workspace 的运行环境和工具链，不暴露敏感值、不修改环境。用于诊断运行前置条件，不用于执行命令。"),
		remoteSession, workspace, mcp.WithArray("sections", mcp.Description("环境信息分区"), mcp.WithStringEnumItems(environment.ValidSections)), mcp.WithString("compare_to", mcp.Description("用于对比的环境快照")), mcp.WithBoolean("save_snapshot", mcp.Description("保存环境快照")),
	), readOnlyToolAnnotation), r.toolEnvironmentInspect)
	r.addTool(s, actionToolWithAnnotation("workspace_state", "读取 Git 状态、差异、快照、工作区变更和项目记忆。action 仅支持 changes、snapshot、diff、watch、memory；非 Git Workspace 返回 git_available=false，但 memory、file_read 和 change_execute 仍可用；本工具只读，不修改文件。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"),
	}, map[string]actionSchemaBranch{
		"changes":  {},
		"snapshot": {},
		"diff":     {Properties: map[string]any{"include_diff": booleanSchema("返回有界 Unified Diff"), "since": stringSchema("由 action=snapshot 返回的快照 ID")}, Required: []string{"since"}},
		"watch":    {Properties: map[string]any{"since": stringSchema("由 action=snapshot 返回的快照 ID")}, Required: []string{"since"}},
		"memory":   {Properties: map[string]any{"keyword": stringSchema("对摘要、结果、下一步、工具和文件变更事实做模糊匹配"), "id": stringSchema("记忆 ID：支持 1、1~10、1,2,3 和混合表达式"), "time": stringSchema("日期筛选：支持 YYYY-MM-DD、日期范围、集合和混合表达式"), "latest": numberSchema("返回最新条数，默认 10，最大 50")}},
	}, readOnlyToolAnnotation), r.toolWorkspaceState)
	r.addTool(s, actionTool("extension_manage", "发现或调用已配置的 Skill 和上游 MCP 扩展。list 可用 query 按名称或描述筛选；name 必须来自 session_open 或 action=list 的返回项，未找到时先用 query=list，不要猜测。仅在 session_open 或选择 Workspace 后使用；不能替代本地文件读取、修改或命令执行。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"), "workspace": stringSchema("已注册的 Workspace 名称"),
	}, map[string]actionSchemaBranch{
		"list":     {Properties: map[string]any{"kind": stringSchema("skill 或 mcp"), "query": stringSchema("按名称和描述匹配的空格分词筛选，如 ui ux 或 ui-ux-pro-max"), "include_tools": booleanSchema("发现上游工具 schema")}},
		"describe": {Properties: map[string]any{"kind": stringSchema("skill 或 mcp"), "name": stringSchema("Skill 名称或 MCP Server 名称"), "server": stringSchema("MCP Server 名称"), "include_tools": booleanSchema("发现上游工具 schema")}, Required: []string{"kind"}},
		"call":     {Properties: map[string]any{"kind": stringSchema("skill 或 mcp"), "name": stringSchema("Skill 名称"), "server": stringSchema("MCP Server 名称"), "tool": stringSchema("上游工具名称"), "arguments": map[string]any{"type": "object", "additionalProperties": true}}, Required: []string{"kind"}},
	}), r.toolExtensionManage)
	r.addTool(s, actionTool("artifact_manage", "注册、列出或读取 Remote Session 产物，用于持久化结果文件和资源；本工具不修改源码、不执行命令。", map[string]any{
		"remote_session_id": stringSchema("持久化的 Remote Session 标识"),
	}, map[string]actionSchemaBranch{
		"list":     {Properties: map[string]any{"kind": stringSchema("产物类型"), "limit": numberSchema("分页数量")}},
		"read":     {Properties: map[string]any{"artifact_id": stringSchema("产物 ID"), "offset": numberSchema("字节偏移"), "limit": numberSchema("字节数量")}, Required: []string{"artifact_id"}},
		"register": {Properties: map[string]any{"path": stringSchema("工作区相对路径"), "name": stringSchema("显示名称"), "kind": stringSchema("产物类型"), "mime_type": stringSchema("MIME 类型")}, Required: []string{"path"}},
	}), r.toolArtifactManage)
	// Special capabilities.
	r.addTool(s, annotatedTool(mcp.NewTool("screenshot_capture", mcp.WithDescription("截取显示器或屏幕区域供视觉检查。用户要求截图时使用；本工具不读取源码、不修改文件、不执行命令。"), remoteSession,
		mcp.WithString("mode", mcp.Description("全屏或区域")), mcp.WithNumber("display", mcp.Description("显示器索引")), mcp.WithNumber("x", mcp.Description("区域 X 坐标")), mcp.WithNumber("y", mcp.Description("区域 Y 坐标")), mcp.WithNumber("width", mcp.Description("区域宽度")), mcp.WithNumber("height", mcp.Description("区域高度")), mcp.WithString("compression", mcp.Description("压缩模式")), mcp.WithString("format", mcp.Description("png 或 jpeg")), mcp.WithNumber("quality", mcp.Description("JPEG 质量")), mcp.WithNumber("max_width", mcp.Description("输出宽度上限")), mcp.WithNumber("max_height", mcp.Description("输出高度上限")),
	), readOnlyToolAnnotation), r.toolScreenshotCapture)
	r.addTool(s, annotatedTool(mcp.NewTool("secrets_provide", mcp.WithDescription("提供仅驻留内存的 Secret 值或引用，以继续已明确批准的操作。不要把 Secret 写入工具结果或日志；本工具不读取或修改源码。"), remoteSession,
		mcp.WithString("secret_id", mcp.Description("可选的待处理 Secret ID")), mcp.WithObject("values", mcp.Description("Secret 名称和值的映射；永不持久化"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
	), secretToolAnnotation), r.toolSecretsProvide)

	r.registerResources(s)
}

// registerConsolidatedToolsV2 is the public catalog. The older registration
// function above is retained as a source-level migration aid only; it is not
// called by registerTools and therefore does not expand tools/list.
func (r *Runtime) registerConsolidatedToolsV2(s *mcpserver.MCPServer) {
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
	_ = json.Unmarshal(operationManage.RawInputSchema, &operationManageSchema)
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
	operationManage.RawInputSchema, _ = json.Marshal(operationManageSchema)
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

	r.addTool(s, publicTool("source_read", "读取文件、搜索内容、列举文件或组装有界上下文；view 明确决定读取语义。", map[string]any{
		"remote_session_id": remoteSession, "view": enumSchema("读取视图", "file", "search", "list", "context"), "path": path, "mode": enumSchema("文件读取模式；mode=full 一次仅一个 path，不能与 items 同用", "window", "full"), "offset": numberSchema("行偏移"), "limit": numberSchema("结果或行数限制"),
		"items": arraySchema(map[string]any{"type": "object", "properties": map[string]any{"path": path, "offset": numberSchema("行偏移"), "limit": numberSchema("行数")}, "required": []string{"path"}}, "批量文件读取项；仅 window 模式使用"), "max_total_bytes": numberSchema("批量读取总预算"),
		"query": stringSchema("搜索条件"), "search_mode": enumSchema("搜索模式", "smart", "exact", "token"), "parallel": booleanSchema("是否并行召回"), "paths": arraySchema(map[string]any{"type": "string"}, "搜索范围"), "include_glob": stringSchema("包含 glob"), "exclude_glob": stringSchema("排除 glob"), "cursor": stringSchema("分页游标"), "max_results": numberSchema("最多匹配文件数"), "max_bytes_per_file": numberSchema("单文件预算"), "regex": booleanSchema("按 RE2 正则解释"), "case_sensitive": booleanSchema("区分大小写"), "include_sha256": booleanSchema("返回文件哈希"), "include_instructions": booleanSchema("返回适用指令"), "context_before": numberSchema("匹配前上下文行数"), "context_after": numberSchema("匹配后上下文行数"),
	}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolSourceRead)

	r.addTool(s, publicTool("change_prepare", "校验文件操作并生成 Changeset 草稿，不直接应用。", map[string]any{"remote_session_id": remoteSession, "summary": stringSchema("变更摘要"), "operations": changeItems, "idempotency_key": stringSchema("业务幂等键"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "operations", "purpose"}, mutatingToolAnnotation), r.toolChangePreparePublic)
	r.addTool(s, publicTool("change_read", "读取 Changeset 差异或历史记录；view 明确决定读取类型。", map[string]any{"remote_session_id": remoteSession, "view": enumSchema("读取视图", "diff", "history"), "changeset_id": stringSchema("Changeset ID"), "limit": numberSchema("历史数量")}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolChangeRead)
	r.addTool(s, publicTool("change_apply", "应用已准备的 Changeset；需要语义确认时使用返回的 confirmation_token 重试。", map[string]any{"remote_session_id": remoteSession, "changeset_id": stringSchema("已准备 Changeset ID；必须原样复制 change_prepare/change_read 返回的值"), "expected_digest": stringSchema("必须原样复制准备 Changeset 返回的 digest；不要填写 diff 统计、tree_digest、snapshot ID、changeset_id 或空值"), "confirmation_token": stringSchema("仅表示用户已确认同一变更，不是认证凭据"), "format": booleanSchema("格式化变更文件"), "verify": arraySchema(map[string]any{"type": "string"}, "验证步骤"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "changeset_id", "expected_digest", "purpose"}, mutatingToolAnnotation), r.toolChangeApply)
	r.addTool(s, publicTool("change_revert", "回滚已应用的 Changeset；需要语义确认时使用返回的 confirmation_token 重试。", map[string]any{"remote_session_id": remoteSession, "changeset_id": stringSchema("待回滚 Changeset ID；必须原样复制已应用 Changeset 的 ID"), "confirmation_token": stringSchema("仅表示用户已确认同一回滚，不是认证凭据"), "purpose": stringSchema("本次调用的目的；必须由用户明确提供")}, []string{"remote_session_id", "changeset_id", "purpose"}, mutatingToolAnnotation), r.toolChangeRevertPublic)

	r.addTool(s, publicTool("command_run", "按工作区命令策略执行用户要求的命令或项目任务；长命令返回 Task ID。", map[string]any{"remote_session_id": remoteSession, "command": stringSchema("用户要求执行的 shell 命令"), "task": stringSchema("已发现的项目任务名称"), "purpose": stringSchema("用户要求执行此命令的原因"), "scope": enumSchema("执行范围", "workspace"), "confirmation_token": stringSchema("仅表示用户已确认同一命令，不是认证凭据"), "yield_time_ms": numberSchema("等待时长")}, []string{"remote_session_id", "purpose"}, commandExecutionToolAnnotation), r.toolCommandRun)
	r.addTool(s, publicTool("task_read", "读取 Task 列表、状态、日志、端口或诊断信息；view 明确决定读取类型。", map[string]any{"remote_session_id": remoteSession, "view": enumSchema("读取视图", "list", "status", "logs", "ports", "diagnostics"), "task_id": stringSchema("Task ID"), "stdout_offset": numberSchema("stdout 偏移"), "stderr_offset": numberSchema("stderr 偏移"), "limit": numberSchema("数量限制"), "yield_time_ms": numberSchema("等待时长")}, []string{"remote_session_id", "view"}, readOnlyToolAnnotation), r.toolTaskRead)
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

func (r *Runtime) registerResources(s *mcpserver.MCPServer) {
	s.AddResourceTemplate(mcp.NewResourceTemplate("mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}", "Remote Session 产物", mcp.WithTemplateDescription("读取已注册的 MCPX 开发产物")), r.resourceArtifact)
	s.AddResourceTemplate(mcp.NewResourceTemplate("mcpx://remote-sessions/{remote_session_id}/changesets/{changeset_id}/diff", "Changeset Unified Diff", mcp.WithTemplateDescription("读取 MCPX Changeset 的完整 Unified Diff"), mcp.WithTemplateMIMEType("text/x-diff")), r.resourceChangesetDiff)
	s.AddResourceTemplate(mcp.NewResourceTemplate("mcpx://remote-sessions/{remote_session_id}/tasks/{task_id}/logs", "终端 Task 日志", mcp.WithTemplateDescription("读取 MCPX 终端 Task 的完整日志"), mcp.WithTemplateMIMEType("text/plain")), r.resourceTaskLogs)
}
