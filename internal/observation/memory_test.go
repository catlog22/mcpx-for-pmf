package observation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseMemoryIDRangesSupportsMixedExpressions(t *testing.T) {
	ranges, err := parseMemoryRanges("1~3,8,10~12", parseMemoryInteger, "id")
	if err != nil {
		t.Fatal(err)
	}
	want := []memoryRange{{Start: 1, End: 3}, {Start: 8, End: 8}, {Start: 10, End: 12}}
	if len(ranges) != len(want) {
		t.Fatalf("ranges=%+v, want %+v", ranges, want)
	}
	for i := range want {
		if ranges[i] != want[i] {
			t.Fatalf("range[%d]=%+v, want %+v", i, ranges[i], want[i])
		}
	}
	for _, input := range []string{"3~1", "1,,2", "zero", "1~2~3"} {
		if _, err := parseMemoryRanges(input, parseMemoryInteger, "id"); err == nil {
			t.Fatalf("invalid range %q was accepted", input)
		}
	}
}

func TestQueryMemoryProjectsStableFieldsAndFiltersWorkspace(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	location := time.FixedZone("HKT", 8*60*60)
	firstAt := time.Date(2026, 8, 1, 16, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(2 * time.Hour)
	progressInput, _ := json.Marshal(map[string]any{
		"current": "完成项目初始化", "result": "创建了核心模块",
		"status": "completed", "next": "运行测试", "related_tool": "edit",
	})
	progressOutput, _ := json.Marshal(map[string]any{
		"status": "ok", "timing": map[string]any{"processing_ms": 999},
		"result": map[string]any{
			"current": "完成项目初始化", "result": "创建了核心模块",
			"status": "completed", "next": "运行测试", "related_tool": "edit",
			"remote_session_id": "secret-session", "resource_uri": "mcpx://secret",
		},
	})
	if _, err := store.Append(context.Background(), Event{
		Workspace: "demo", RemoteSessionID: "rs-demo", Tool: "progress", Type: TypeToolCompleted,
		Input: progressInput, Output: progressOutput, Summary: "progress ok", CreatedAt: firstAt,
	}); err != nil {
		t.Fatal(err)
	}
	fileOutput, _ := json.Marshal(map[string]any{
		"edit_id": "edit_1", "resource_uri": "mcpx://secret",
		"results": []any{map[string]any{"path": "src/main.go", "operation": "create", "new_path": ""}},
	})
	if _, err := store.Append(context.Background(), Event{
		Workspace: "demo", Type: TypeFileChanged, Output: fileOutput,
		Summary: "创建项目文件", CreatedAt: secondAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), Event{
		Workspace: "other", RemoteSessionID: "rs-other", Tool: "progress", Type: TypeToolCompleted,
		Input: progressInput, Output: progressOutput, Summary: "其他 Workspace", CreatedAt: secondAt,
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.QueryMemory(context.Background(), MemoryQuery{
		Workspace: "demo", Keyword: "MAIN.GO", Time: "2026-08-02", Latest: 10, Location: location,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.HasMore || len(page.Items) != 1 {
		t.Fatalf("page=%+v", page)
	}
	item := page.Items[0]
	if item.Type != "file_changed" || item.Summary != "创建项目文件" || len(item.Files) != 1 || item.Files[0].Path != "src/main.go" {
		t.Fatalf("item=%+v", item)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"remote_session_id", "resource_uri", "processing_ms", "secret-session"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("memory leaked %q: %s", forbidden, text)
		}
	}

	page, err = store.QueryMemory(context.Background(), MemoryQuery{
		Workspace: "demo", SessionID: "rs-demo", Type: "progress", Keyword: "项目初始化", ID: "1", Latest: 1, Location: location,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Status != "completed" || page.Items[0].Next != "运行测试" || page.Items[0].Result != "创建了核心模块" {
		t.Fatalf("progress page=%+v", page)
	}
	wrongSession, err := store.QueryMemory(context.Background(), MemoryQuery{
		Workspace: "demo", SessionID: "rs-other", Type: "progress", Latest: 1, Location: location,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wrongSession.Total != 0 || len(wrongSession.Items) != 0 {
		t.Fatalf("session filter leaked progress: %+v", wrongSession)
	}
}

func TestQueryMemoryLatestAndDateRange(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	location := time.FixedZone("HKT", 8*60*60)
	for i := 0; i < 3; i++ {
		at := time.Date(2026, 8, 1+i, 1, 0, 0, 0, time.UTC)
		if _, err := store.Append(context.Background(), Event{
			Workspace: "demo", Type: TypeSessionLifecycle, Summary: "会话事件", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.QueryMemory(context.Background(), MemoryQuery{
		Workspace: "demo", Time: "2026-08-01~2026-08-02", Latest: 1, Location: location,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || !page.HasMore || len(page.Items) != 1 {
		t.Fatalf("page=%+v", page)
	}
}
