package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/arc"
	"mcpx/internal/envelope"
	"mcpx/internal/logging"
)

func (r *Runtime) addTool(s *mcp.Server, tool mcp.Tool, handler mcp.ToolHandler) {
	tool = requireIntentSchema(tool)
	// OutputSchema describes structuredContent, not the larger ARC metadata
	// envelope. The shared ARC contract stays identical across tools while
	// hard limits are attached from the same source used by runtime capabilities.
	tool.OutputSchema = outputSchemaForTool(tool.Name)
	instrumented := r.instrumentTool(tool.Name, handler)
	if r.toolHandlers == nil {
		r.toolHandlers = map[string]mcp.ToolHandler{}
	}
	if r.toolMeta == nil {
		r.toolMeta = map[string]toolAnnotation{}
	}
	if r.toolIndex == nil {
		r.toolIndex = map[string]mcp.Tool{}
	}
	ann := toolAnnotation{}
	if tool.Annotations != nil {
		ann = toolAnnotation{
			ReadOnly:    tool.Annotations.ReadOnlyHint,
			Destructive: boolPointerValue(tool.Annotations.DestructiveHint),
			Idempotent:  tool.Annotations.IdempotentHint,
			OpenWorld:   boolPointerValue(tool.Annotations.OpenWorldHint),
		}
	}
	r.toolHandlers[tool.Name] = instrumented
	r.toolMeta[tool.Name] = ann
	r.toolIndexMu.Lock()
	r.toolIndex[tool.Name] = tool
	r.toolIndexMu.Unlock()
	tt := tool
	s.AddTool(&tt, instrumented)
}

func outputSchemaForTool(toolName string) json.RawMessage {
	base := arc.OutputSchema()
	limits, ok := publishedLimits()[toolName]
	if !ok {
		return base
	}
	var schema map[string]any
	if err := json.Unmarshal(base, &schema); err != nil || schema == nil {
		return base
	}
	schema["x-mcpx-limits"] = limits
	encoded, err := json.Marshal(schema)
	if err != nil {
		return base
	}
	return json.RawMessage(encoded)
}

func boolPointerValue(value *bool) bool {
	return value != nil && *value
}

func requireIntentSchema(tool mcp.Tool) mcp.Tool {
	strictActions := map[string]bool{}
	if tool.Meta != nil {
		switch values := tool.Meta["mcpx/strict_action_arguments"].(type) {
		case []string:
			for _, value := range values {
				strictActions[strings.TrimSpace(value)] = true
			}
		case []any:
			for _, raw := range values {
				if value, ok := raw.(string); ok {
					strictActions[strings.TrimSpace(value)] = true
				}
			}
		}
	}
	goal := map[string]any{
		"type":        "string",
		"description": "本轮工作的总体目标；只填写当前任务需要保持的目标",
	}
	purpose := map[string]any{
		"type":        "string",
		"description": purposeDescription(tool.Name),
	}
	reasoningSummary := map[string]any{
		"type":        "string",
		"description": "简短、可展示的判断依据或操作理由；不要填写隐藏思维链",
	}
	progressSummary := map[string]any{
		"type":        "string",
		"description": "上一工具调用后的可验证进度摘要、结果和下一步；没有下一次工具调用时请使用 progress_summary",
	}
	nextStep := map[string]any{
		"type":        "string",
		"description": "本次操作完成后的下一项具体计划；没有后续动作时留空",
	}
	planID := map[string]any{
		"type":        "string",
		"description": "关联的服务端 Plan ID",
	}
	planTaskID := map[string]any{
		"type":        "string",
		"description": "关联的服务端 Plan Task ID",
	}
	executionTaskID := map[string]any{
		"type":        "string",
		"description": "关联的服务端执行 Task ID",
	}
	rawBytes := mcpresult.ToolSchemaJSON(tool)
	var raw map[string]any
	if err := json.Unmarshal(rawBytes, &raw); err != nil || raw == nil {
		raw = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	properties, _ := raw["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties["goal"] = goal
	properties["purpose"] = purpose
	properties["reasoning_summary"] = reasoningSummary
	properties["progress_summary"] = progressSummary
	properties["next_step"] = nextStep
	properties["plan_id"] = planID
	properties["plan_task_id"] = planTaskID
	properties["execution_task_id"] = executionTaskID
	raw["type"] = "object"
	raw["properties"] = properties
	if branches, ok := raw["oneOf"].([]any); ok {
		for _, rawBranch := range branches {
			branch, ok := rawBranch.(map[string]any)
			if !ok {
				continue
			}
			branchProperties, _ := branch["properties"].(map[string]any)
			if branchProperties == nil {
				branchProperties = map[string]any{}
			}
			action := ""
			if actionSchema, ok := branchProperties["action"].(map[string]any); ok {
				action, _ = actionSchema["const"].(string)
			}
			if strictActions[action] {
				branch["properties"] = branchProperties
				continue
			}
			branchProperties["goal"] = goal
			branchProperties["purpose"] = purpose
			branchProperties["reasoning_summary"] = reasoningSummary
			branchProperties["progress_summary"] = progressSummary
			branchProperties["next_step"] = nextStep
			branchProperties["plan_id"] = planID
			branchProperties["plan_task_id"] = planTaskID
			branchProperties["execution_task_id"] = executionTaskID
			branch["properties"] = branchProperties
		}
	}
	if encoded, marshalErr := json.Marshal(raw); marshalErr == nil {
		tool.InputSchema = json.RawMessage(encoded)
	}
	return tool
}

func purposeDescription(toolName string) string {
	const base = "用一句简短、具体的话说明本次操作、对象和目的；只陈述真实语义，避免重复 goal/reasoning。"
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read", "observe", "runtime_read", "environment_read", "discover":
		return base + " 只读操作应明确仅读取/检查，不修改 Workspace 或系统状态。"
	case "execute":
		return base + " 按命令真实副作用描述，不按语言套模板：版本/帮助/纯检查只有确实无写入时才声明只读；编译/静态检查应说明可能产生的缓存或构建产物及不会发生的安装/系统替换；pytest、python -m unittest、npm/pnpm/yarn test、cargo test、mvn/gradle test、dotnet test 等会执行项目代码，不得称为只读；npm/pnpm/yarn install、pip/poetry、cargo fetch、mvn/gradle 等依赖动作应说明依赖或缓存写入；push/release/deploy/install/delete 等外部或破坏性动作仅在用户已明确要求或确认时写明用户已授权，并限定具体目标。禁止为通过安全检查虚构安全、只读或授权。"
	case "edit":
		return base + " 明确要修改的 Workspace 文件和变更目的；用户已明确要求该修改时应写明按用户要求/已授权，并注明 edit 不执行删除；禁止虚构授权。"
	case "move_out":
		return base + " prepare 为预览时明确仅冻结/预览、不移动；用户明确要求删除/移除时从 prepare 起写明用户已授权将明确目标安全移至隔离区；submit 只使用已确认的 confirmation_uuid；禁止虚构授权。"
	case "session", "plan", "operation_batch", "operation_manage", "environment", "artifact":
		return base + " 会改变 MCPX 会话、计划或元数据状态时准确说明变更范围；只有用户已明确要求时才表述为用户授权。"
	case "skill_call", "mcp_call":
		return base + " 明确上游调用会执行什么；若可能产生外部副作用，只有用户已明确要求或确认时才写明授权和作用范围。"
	case "screenshot_capture":
		return base + " 明确仅截取用户请求的显示器或区域，不修改 Workspace。"
	case "secret_provide":
		return base + " 明确仅向当前会话内存提供用户给出的 Secret，不写入结果或日志。"
	default:
		return base + " 如操作存在写入或外部副作用，只有用户已明确要求或确认时才写明授权和具体范围；禁止虚构安全或授权。"
	}
}

func appendRequired(value any, required string) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items)+1)
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return appendRequiredStrings(result, required)
}

func appendRequiredStrings(required []string, wanted string) []string {
	for _, item := range required {
		if item == wanted {
			return required
		}
	}
	return append(required, wanted)
}

type interactionTiming struct {
	StartedAtMs      int64
	ReceivedAtMs     int64
	CompletedAtMs    int64
	NetworkLatencyMs int64
	ProcessingMs     int64
	ServerElapsedMs  int64
}

func (r *Runtime) instrumentTool(name string, handler mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		// Keep the entire instrumentation boundary defensive. Handler calls use
		// callToolSafely below so normal panics retain ARC wrapping; this outer
		// guard also covers malformed observation metadata or renderer changes.
		defer func() {
			if recovered := recover(); recovered != nil {
				logging.With("component", "mcp_tool").Error("instrumentation panic recovered", "tool", name, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				result = mcpresult.NewError("EXECUTION_RUNTIME_ERROR: tool execution failed")
				err = nil
			}
		}()
		received := time.Now()
		callCtx, runtime := ensureRuntimeContext(ctx, mcpresult.Header(req), received)
		clientName, clientVersion := clientInfoFromContext(callCtx)
		if clientName != "" && clientName != "unknown" {
			runtime = runtimeContextWithClient(runtime, clientName, clientVersion)
		}
		callCtx = withRuntimeContext(callCtx, runtime)
		callCtx = withToolInvocationName(callCtx, name)
		if isCleanPublicTool(name) {
			callCtx = withCleanCoreRequest(callCtx)
		}
		internalOperationStep := isOperationChild(callCtx)
		observationRequest, observationParseErr := r.parseEnv(callCtx, req)
		if !internalOperationStep && observationParseErr == nil && r.observation != nil {
			// Async: never blocks tools/call on Store.Append.
			_ = r.observation.RecordToolStarted(callCtx, name, observationRequest, mcpresult.Arguments(req))
		}

		if !isOperationChild(callCtx) && r.operations != nil && asyncEligibleTool(name) && executionMode(req) == "async" && observationParseErr == nil {
			result, err = callToolSafely(name, func() (*mcp.CallToolResult, error) {
				return r.submitAsyncTool(callCtx, name, req, observationRequest)
			})
		} else {
			result, err = callToolSafely(name, func() (*mcp.CallToolResult, error) {
				return handler(callCtx, req)
			})
		}
		completed := time.Now()
		timing := makeInteractionTiming(runtime.StartedAtMs, received, completed)
		runtime = runtimeContextWithTiming(runtime, timing)
		status := "ok"
		if err != nil || result == nil || result.IsError {
			status = "error"
		}
		if err != nil {
			if result == nil {
				result = mcpresult.NewError(err.Error())
			} else {
				result.IsError = true
			}
		}
		// Wrap first so host-visible content is the human summary; observation
		// then snapshots that text only (never full structuredContent dump).
		result = arc.WrapToolResult(name, arc.ResultContext{
			RequestID: runtime.RequestID, TraceID: runtime.TraceID, SpanID: runtime.SpanID,
			Context: arc.Context{
				Goal: observationRequest.Goal, Purpose: firstSemanticPurpose(observationRequest),
				ReasoningSummary: observationRequest.ReasoningSummary,
				ProgressSummary:  observationRequest.ProgressSummary, NextStep: observationRequest.NextStep,
				PlanID: observationRequest.PlanID, PlanTaskID: observationRequest.PlanTaskID, ExecutionTaskID: observationRequest.ExecutionTaskID, OperationID: observationRequest.OperationID,
			},
			Timing: arc.Timing{
				StartedAtMs: timing.StartedAtMs, ReceivedAtMs: timing.ReceivedAtMs,
				CompletedAtMs: timing.CompletedAtMs, NetworkLatencyMs: timing.NetworkLatencyMs,
				ProcessingMs: timing.ProcessingMs, ServerElapsedMs: timing.ServerElapsedMs,
			},
		}, result)
		if !internalOperationStep && observationParseErr == nil && r.observation != nil {
			_ = r.observation.RecordToolCompleted(callCtx, name, observationRequest, mcpresult.Arguments(req), result, err, timing)
		}
		if !internalOperationStep {
			logToolCall(name, runtime, status, timing)
		}
		return result, err
	}
}

func firstSemanticPurpose(req envelope.Request) string {
	if strings.TrimSpace(req.Purpose) != "" {
		return req.Purpose
	}
	return req.Intent
}

// callToolSafely keeps a handler panic inside the MCP tool error contract. A
// malformed request, task race, or future handler regression must not take
// down the shared MCP server process or its other sessions.
func callToolSafely(name string, call func() (*mcp.CallToolResult, error)) (result *mcp.CallToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.With("component", "mcp_tool").Error("panic recovered", "tool", name, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			result = mcpresult.NewError("EXECUTION_RUNTIME_ERROR: tool execution failed")
			err = nil
		}
	}()
	return call()
}

func makeInteractionTiming(startedAtMs int64, received, completed time.Time) interactionTiming {
	receivedAtMs := received.UnixMilli()
	completedAtMs := completed.UnixMilli()
	return interactionTiming{
		StartedAtMs: startedAtMs, ReceivedAtMs: receivedAtMs, CompletedAtMs: completedAtMs,
		NetworkLatencyMs: receivedAtMs - startedAtMs,
		ProcessingMs:     completedAtMs - receivedAtMs,
		ServerElapsedMs:  completedAtMs - startedAtMs,
	}
}

func logToolCall(name string, runtime RuntimeContext, status string, timing interactionTiming) {
	fields := []any{
		"tool", name, "status", status,
		"request_id", runtime.RequestID, "trace_id", runtime.TraceID, "span_id", runtime.SpanID,
		"started_at_ms", timing.StartedAtMs, "received_at_ms", timing.ReceivedAtMs,
		"completed_at_ms", timing.CompletedAtMs, "network_latency_ms", timing.NetworkLatencyMs,
		"processing_ms", timing.ProcessingMs, "server_elapsed_ms", timing.ServerElapsedMs,
	}
	if runtime.ClientName != "" {
		fields = append(fields, "client_name", runtime.ClientName, "client_version", runtime.ClientVersion)
	}
	logging.With("component", "mcp_tool").Info("call", fields...)
}

type accessLogResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *accessLogResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessLogResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *accessLogResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *accessLogResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (g *Gateway) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestContext, runtime := ensureRuntimeContext(r.Context(), r.Header, started)
		r = r.WithContext(requestContext)
		w.Header().Set("X-Request-ID", runtime.RequestID)
		w.Header().Set("X-MCPX-Trace-ID", runtime.TraceID)
		w.Header().Set("X-MCPX-Span-ID", runtime.SpanID)
		w.Header().Add("Trailer", "Server-Timing")
		w.Header().Add("Trailer", "X-MCPX-Processing-Ms")
		logged := &accessLogResponseWriter{ResponseWriter: w}
		next.ServeHTTP(logged, r)
		status := logged.status
		if status == 0 {
			status = http.StatusOK
		}
		processingMs := time.Since(started).Milliseconds()
		logged.Header().Set("Server-Timing", fmt.Sprintf("mcpx;dur=%d", processingMs))
		logged.Header().Set("X-MCPX-Processing-Ms", strconv.FormatInt(processingMs, 10))
		logging.With("component", "mcp_http").Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"request_id", runtime.RequestID,
			"trace_id", runtime.TraceID,
			"span_id", runtime.SpanID,
			"duration_ms", processingMs,
			"response_bytes", logged.bytes,
			"mcp_session_id", r.Header.Get("Mcp-Session-Id"),
			"remote_addr", r.RemoteAddr,
		)
	})
}
