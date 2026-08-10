package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/config"
	"mcpx/internal/logging"
)

func TestGatewayAccessLogRecordsDuration(t *testing.T) {
	var output bytes.Buffer
	logging.Init(logging.Options{Level: "info", Format: "text", Out: &output})
	defer logging.Init(logging.Options{Level: "info"})

	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	})
	server := httptest.NewServer(NewGateway(cfg, nil, inner).Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.Trailer.Get("Server-Timing") == "" || response.Trailer.Get("X-MCPX-Processing-Ms") == "" {
		t.Fatalf("missing response timing trailers: %+v", response.Trailer)
	}
	log := output.String()
	if !strings.Contains(log, "component=mcp_http") || !strings.Contains(log, "duration_ms=") || !strings.Contains(log, "status=200") {
		t.Fatalf("missing HTTP access log fields: %s", log)
	}
}

func TestToolLogRecordsDuration(t *testing.T) {
	var output bytes.Buffer
	logging.Init(logging.Options{Level: "info", Format: "text", Out: &output})
	defer logging.Init(logging.Options{Level: "info"})

	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("ok"), nil
	}
	instrumented := (&Runtime{}).instrumentTool("observability_test", handler)
	request := mcpresult.Request(map[string]any{})

	result, err := instrumented(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Meta == nil || result.Meta["mcpx.processing_ms"] == nil {
		t.Fatalf("missing tool processing metadata: %+v", result.Meta)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("ARC content type = %T", result.Content[0])
	}
	if text.Text != "ok" {
		t.Fatalf("non-code-change text should stay the summary, got: %q", text.Text)
	}
	payload := decodeARCEnvelope(t, result)
	if payload["mcpx"] == nil || payload["ok"] != nil {
		t.Fatalf("tool result is not ARC-only: %+v", payload)
	}
	log := output.String()
	if !strings.Contains(log, "component=mcp_tool") || !strings.Contains(log, "tool=observability_test") || !strings.Contains(log, "processing_ms=") || !strings.Contains(log, "network_latency_ms=") {
		t.Fatalf("missing tool timing log fields: %s", log)
	}
}

func TestToolRequestStartedAtMsPrefersClientMeta(t *testing.T) {
	received := time.UnixMilli(1786365000200)
	request := mcpresult.Request(map[string]any{"started_at_ms": int64(1786365000100)})
	request.Params.Meta = mcp.Meta{clientStartedAtMetaKey: int64(1786365000050)}
	if got := toolRequestStartedAtMs(request, received); got != int64(1786365000050) {
		t.Fatalf("started_at_ms = %d, want client meta timestamp", got)
	}
}

func TestInstrumentToolSendsNativeProgressHeartbeat(t *testing.T) {
	old := toolProgressHeartbeatInterval
	toolProgressHeartbeatInterval = 5 * time.Millisecond
	defer func() { toolProgressHeartbeatInterval = old }()

	protocol := mcp.NewServer(&mcp.Implementation{Name: "progress-server", Version: "0.1.0"}, nil)
	handler := (&Runtime{}).instrumentTool("slow_tool", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		time.Sleep(20 * time.Millisecond)
		return mcpresult.NewText("done"), nil
	})
	protocol.AddTool(&mcp.Tool{Name: "slow_tool", InputSchema: map[string]any{"type": "object"}}, handler)

	progress := make(chan *mcp.ProgressNotificationParams, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "progress-client", Version: "0.1.0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			if req != nil && req.Params != nil {
				progress <- req.Params
			}
		},
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	serverSession, err := protocol.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	params := &mcp.CallToolParams{Name: "slow_tool", Arguments: map[string]any{"started_at_ms": time.Now().UnixMilli()}}
	params.SetProgressToken("heartbeat-1")
	if _, err := clientSession.CallTool(ctx, params); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-progress:
		if update.ProgressToken != "heartbeat-1" || !strings.Contains(update.Message, "slow_tool is still running") {
			t.Fatalf("unexpected progress update: %+v", update)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("server did not send native progress heartbeat")
	}
}

func TestInstrumentToolCarriesSemanticContextToARC(t *testing.T) {
	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewStructured(map[string]any{"status": "succeeded"}, "ok"), nil
	}
	instrumented := (&Runtime{}).instrumentTool("observability_context", handler)
	request := mcpresult.Request(map[string]any{
		"goal":              "提升观测体验",
		"purpose":           "验证 ARC 语义上下文",
		"reasoning_summary": "先验证请求到结果的透传",
		"progress_summary":  "工具调用已完成",
		"next_step":         "检查终端渲染",
		"plan_id":           "pl_context",
		"plan_task_id":      "pt_context",
		"execution_task_id": "task_context",
	})
	result, err := instrumented(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent=%T", result.StructuredContent)
	}
	semantic, ok := structured["context"].(map[string]any)
	if !ok {
		t.Fatalf("context=%#v", structured["context"])
	}
	for key, want := range map[string]string{
		"goal": "提升观测体验", "purpose": "验证 ARC 语义上下文", "reasoning_summary": "先验证请求到结果的透传",
		"progress_summary": "工具调用已完成", "next_step": "检查终端渲染", "plan_id": "pl_context", "plan_task_id": "pt_context", "execution_task_id": "task_context",
	} {
		if semantic[key] != want {
			t.Fatalf("context[%q]=%v, want %q", key, semantic[key], want)
		}
	}
	if _, exists := semantic["task_id"]; exists {
		t.Fatalf("ambiguous task_id leaked into ARC context: %+v", semantic)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "Context:") || !strings.Contains(text, "next: 检查终端渲染") {
		t.Fatalf("human ARC text=%q", text)
	}
}

func TestCallToolSafelyConvertsHandlerPanic(t *testing.T) {
	result, err := callToolSafely("panic_test", func() (*mcp.CallToolResult, error) {
		panic("synthetic handler panic")
	})
	if err != nil {
		t.Fatalf("panic should be converted to a tool result, got error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("panic result=%+v, want structured MCP tool error", result)
	}
	if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "EXECUTION_RUNTIME_ERROR: tool execution failed" {
		t.Fatalf("panic result content=%+v", result.Content)
	}

	result, err = callToolSafely("recovery_test", func() (*mcp.CallToolResult, error) {
		return mcpresult.NewText("alive"), nil
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("safe call did not remain usable after panic: result=%+v err=%v", result, err)
	}
}

func TestStartupSkillDetailsUseDebugAndInfoOnlyCounts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo", "fyy", "codex")
	skillRoot := t.TempDir()
	skillDir := filepath.Join(skillRoot, "sample")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: sample\ndescription: sample skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt.cfg.Discovery.Skills.Dirs = []string{skillRoot}
	recorder := &startupLogRecorder{}
	rt.logStartupInventory(recorder)
	if !strings.Contains(recorder.info.String(), "skills_summary count 1") {
		t.Fatalf("info log should contain only skill count: %s", recorder.info.String())
	}
	if strings.Contains(recorder.info.String(), "sample") {
		t.Fatalf("skill detail leaked into info log: %s", recorder.info.String())
	}
	if !strings.Contains(recorder.debug.String(), "sample") {
		t.Fatalf("debug log should contain skill detail: %s", recorder.debug.String())
	}
}

type startupLogRecorder struct {
	info  bytes.Buffer
	debug bytes.Buffer
}

func (r *startupLogRecorder) Info(msg string, args ...any) {
	r.info.WriteString(msg)
	for _, arg := range args {
		r.info.WriteString(" ")
		r.info.WriteString(fmt.Sprint(arg))
	}
	r.info.WriteByte('\n')
}

func (r *startupLogRecorder) Debug(msg string, args ...any) {
	r.debug.WriteString(msg)
	for _, arg := range args {
		r.debug.WriteString(" ")
		r.debug.WriteString(fmt.Sprint(arg))
	}
	r.debug.WriteByte('\n')
}
