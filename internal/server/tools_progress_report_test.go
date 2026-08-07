package server

import (
	"mcpx/internal/mcpresult"

	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
)

func TestProgressReportRecordsPauseSummaryAndPreviousResult(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := mcpresult.Request(map[string]any{
		"intent":            "告知用户当前进度并等待确认",
		"progress_summary":  "已完成文件读取，当前不再继续调用工具",
		"remote_session_id": created.Session.ID,
		"summary":           "已定位到供应商表单问题",
		"result_summary":    "file_read 返回了目标字段代码",
		"status":            "waiting_for_user",
		"next_step":         "请用户确认是否继续修改",
		"related_tool":      "file_read",
	})

	wrapped := rt.instrumentTool("progress_report", rt.toolProgressReport)
	result, err := wrapped(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("progress report returned an error: %+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("result content type=%T", result.Content[0])
	}
	if !strings.Contains(text.Text, "已定位到供应商表单问题") || !strings.Contains(text.Text, "请用户确认是否继续修改") {
		t.Fatalf("progress report display=%q", text.Text)
	}

	var started, completed bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		started, completed = false, false
		for _, event := range events {
			if event.Tool != "progress_report" {
				continue
			}
			if event.Type == "tool.started" {
				started = event.ProgressSummary == "已完成文件读取，当前不再继续调用工具"
			}
			if event.Type == "tool.completed" {
				// Human observation carries summary text, not full structuredContent.
				completed = strings.Contains(string(event.Output), "已定位到供应商表单问题") ||
					strings.Contains(event.Summary, "progress_report")
			}
		}
		if started && completed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started || !completed {
		t.Fatalf("progress report was not fully recorded: started=%v completed=%v", started, completed)
	}
}

func TestProgressReportAllowsResultSummaryUpToConfiguredLimit(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	wrapped := rt.instrumentTool("progress_report", rt.toolProgressReport)
	request := mcpresult.Request(map[string]any{})
	arguments := map[string]any{
		"intent":            "验证结果摘要长度限制",
		"progress_summary":  "正在验证",
		"remote_session_id": created.Session.ID,
		"summary":           "已完成长度限制验证",
		"result_summary":    strings.Repeat("x", envelope.MaxResultSummaryBytes),
		"status":            "completed",
	}
	request = mcpresult.Request(arguments)
	result, err := wrapped(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result summary at limit was rejected: %+v", result)
	}

	arguments["result_summary"] = strings.Repeat("x", envelope.MaxResultSummaryBytes+1)
	request = mcpresult.Request(arguments)
	result, err = wrapped(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	want := fmt.Sprintf("result summary exceeds %d bytes", envelope.MaxResultSummaryBytes)
	// After ARC wrap, human text is a short summary of the error body.
	if !ok || (!strings.Contains(text.Text, want) && !strings.Contains(fmt.Sprint(result.StructuredContent), want)) {
		t.Fatalf("unexpected over-limit error: text=%q result=%+v", text.Text, result)
	}
}
