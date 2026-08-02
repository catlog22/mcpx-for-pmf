package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
)

func TestWorkspaceStateMemoryReturnsBoundedFacts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, _ := rt.reg.Get("demo")
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"summary": "完成初始化", "result_summary": "创建核心文件", "status": "completed", "next_step": "运行测试",
	})
	output, _ := json.Marshal(map[string]any{
		"result": map[string]any{
			"summary": "完成初始化", "result_summary": "创建核心文件", "status": "completed", "next_step": "运行测试",
		},
	})
	if _, err := rt.observation.store.Append(context.Background(), observation.Event{
		Workspace: "demo", RemoteSessionID: created.Session.ID, Tool: "progress_report",
		Type: observation.TypeToolCompleted, Input: input, Output: output,
		Summary: "progress_report ok", CreatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent": "查询项目记忆", "action": "memory", "remote_session_id": created.Session.ID,
		"keyword": "核心文件", "latest": float64(10),
	}
	result, err := rt.toolWorkspaceState(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeToolResult(t, result)
	if envelope["status"] != "ok" {
		t.Fatalf("memory response=%+v", envelope)
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("memory data=%T %+v", envelope["data"], envelope)
	}
	items, ok := data["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("memory items=%T %+v", data["items"], data)
	}
	item := items[0].(map[string]any)
	if item["type"] != "progress" || item["status"] != "completed" || item["next"] != "运行测试" {
		t.Fatalf("memory item=%+v", item)
	}
	encoded, _ := json.Marshal(item)
	if string(encoded) == "" {
		t.Fatal("empty memory item")
	}
}

func TestWorkspaceStateMemoryRejectsInvalidLatest(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, _ := rt.reg.Get("demo")
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent": "查询项目记忆", "action": "memory", "remote_session_id": created.Session.ID,
		"latest": float64(51),
	}
	result, err := rt.toolWorkspaceState(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeToolResult(t, result)
	if envelope["status"] != "error" {
		t.Fatalf("invalid latest response=%+v", envelope)
	}
	errorObject, _ := envelope["error"].(map[string]any)
	if errorObject["code"] != "BAD_REQUEST" {
		t.Fatalf("invalid latest error=%+v", errorObject)
	}
}
