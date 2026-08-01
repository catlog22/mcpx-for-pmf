package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

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

	handler := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	instrumented := (&Runtime{}).instrumentTool("observability_test", handler)
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{}
	result, err := instrumented(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Meta == nil || result.Meta.AdditionalFields["mcpx.processing_ms"] == nil {
		t.Fatalf("missing tool processing metadata: %+v", result.Meta)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("ARC content type = %T", result.Content[0])
	}
	if text.Text != "ok" {
		t.Fatalf("non-code-change text should stay the summary, got: %q", text.Text)
	}
	if result.StructuredContent == nil {
		t.Fatal("ARC structured content missing")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["mcpx"] == nil || payload["ok"] != nil {
		t.Fatalf("tool result is not ARC-only: %+v", payload)
	}
	log := output.String()
	if !strings.Contains(log, "component=mcp_tool") || !strings.Contains(log, "tool=observability_test") || !strings.Contains(log, "processing_ms=") || !strings.Contains(log, "network_latency_ms=") {
		t.Fatalf("missing tool timing log fields: %s", log)
	}
}

func TestStartupSkillDetailsUseDebugAndInfoOnlyCounts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
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
	if !strings.Contains(recorder.info.String(), "skills_summary") || !strings.Contains(recorder.info.String(), "1") {
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
