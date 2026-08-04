package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/arc"
	"mcpx/internal/logging"
)

func (r *Runtime) addTool(s *mcpserver.MCPServer, tool mcp.Tool, handler mcpserver.ToolHandlerFunc) {
	tool = requireIntentSchema(tool)
	tool.OutputSchema = mcp.ToolOutputSchema{}
	tool.RawOutputSchema = arc.OutputSchema()
	s.AddTool(tool, r.instrumentTool(tool.Name, handler))
}

func requireIntentSchema(tool mcp.Tool) mcp.Tool {
	purpose := map[string]any{
		"type":        "string",
		"description": "本次调用的用户目标或语义用途；高风险工具会要求填写",
	}
	progressSummary := map[string]any{
		"type":        "string",
		"description": "上一工具调用后的可验证进度摘要、结果和下一步；没有下一次工具调用时请使用 progress_report",
	}
	if len(tool.RawInputSchema) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(tool.RawInputSchema, &raw); err == nil {
			properties, _ := raw["properties"].(map[string]any)
			if properties == nil {
				properties = map[string]any{}
			}
			properties["purpose"] = purpose
			properties["progress_summary"] = progressSummary
			raw["type"] = "object"
			raw["properties"] = properties
			if encoded, marshalErr := json.Marshal(raw); marshalErr == nil {
				tool.RawInputSchema = encoded
			}
		}
		return tool
	}
	tool.InputSchema.Type = "object"
	if tool.InputSchema.Properties == nil {
		tool.InputSchema.Properties = map[string]any{}
	}
	tool.InputSchema.Properties["purpose"] = purpose
	tool.InputSchema.Properties["progress_summary"] = progressSummary
	return tool
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

func (r *Runtime) instrumentTool(name string, handler mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		received := time.Now()
		callCtx, runtime := ensureRuntimeContext(ctx, req.Header, received)
		clientName, clientVersion := clientInfoFromContext(callCtx)
		if clientName != "" && clientName != "unknown" {
			runtime = runtimeContextWithClient(runtime, clientName, clientVersion)
		}
		callCtx = withRuntimeContext(callCtx, runtime)
		callCtx = withToolInvocationName(callCtx, name)
		observationRequest, observationParseErr := r.parseEnv(callCtx, req)
		if observationParseErr == nil && r.observation != nil {
			_ = r.observation.RecordToolStarted(callCtx, name, observationRequest, req.GetArguments())
		}

		result, err := handler(callCtx, req)
		completed := time.Now()
		timing := makeInteractionTiming(runtime.StartedAtMs, received, completed)
		runtime = runtimeContextWithTiming(runtime, timing)
		status := "ok"
		if err != nil || result == nil || result.IsError {
			status = "error"
		}
		if observationParseErr == nil && r.observation != nil {
			_ = r.observation.RecordToolCompleted(callCtx, name, observationRequest, req.GetArguments(), result, err, timing)
		}
		if err != nil {
			if result == nil {
				result = mcp.NewToolResultError(err.Error())
			} else {
				result.IsError = true
			}
		}
		result = arc.WrapToolResult(name, arc.ResultContext{
			RequestID: runtime.RequestID, TraceID: runtime.TraceID, SpanID: runtime.SpanID,
			Timing: arc.Timing{
				StartedAtMs: timing.StartedAtMs, ReceivedAtMs: timing.ReceivedAtMs,
				CompletedAtMs: timing.CompletedAtMs, NetworkLatencyMs: timing.NetworkLatencyMs,
				ProcessingMs: timing.ProcessingMs, ServerElapsedMs: timing.ServerElapsedMs,
			},
		}, result)
		logToolCall(name, runtime, status, timing)
		return result, err
	}
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
