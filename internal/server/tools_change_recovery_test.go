package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/remotesession"
)

func TestChangeExecutePersistsDiffFile(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"summary":           "persist diff",
		"operations": []map[string]any{
			{"operation": "create", "path": "hello.go", "content": "package demo\n\nconst Hello = 1\n"},
		},
	}
	result, err := rt.toolChangeExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	data, _ := response["data"].(map[string]any)
	diffFile, _ := data["diff_file"].(string)
	if !strings.HasPrefix(diffFile, ".mcpx/diffs/") || !strings.HasSuffix(diffFile, ".diff") {
		t.Fatalf("change result must expose a persisted diff file: %+v", data)
	}
	if _, err := os.Stat(filepath.Join(registered.Path, filepath.FromSlash(diffFile))); err != nil {
		t.Fatalf("persisted diff file missing: %v", err)
	}
}

func TestChangeExecutePatchTooLargeHasRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Files.MaxPatchLines = 10
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	content := strings.Repeat("line\n", 20)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"summary":           "oversized patch",
		"operations": []map[string]any{
			{"operation": "create", "path": "big.txt", "content": content},
		},
	}
	result, err := rt.toolChangeExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	if response["status"] != "denied" {
		t.Fatalf("oversized patch was not denied: %+v", response)
	}
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "PATCH_TOO_LARGE" {
		t.Fatalf("unexpected error code: %v", errorBody["code"])
	}
	data, _ := response["data"].(map[string]any)
	if data["max_patch_lines"] != float64(10) {
		t.Fatalf("max_patch_lines missing from details: %+v", data)
	}
	actions, _ := data["next_actions"].([]any)
	if len(actions) == 0 {
		t.Fatalf("patch_too_large must carry split guidance: %+v", data)
	}
}

func TestChangeExecuteMissingRevisionHasRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"summary":           "replace without revision",
		"operations": []map[string]any{
			{"operation": "replace_range", "path": "demo.go", "range_start": 1, "range_end": 1, "content": "package demo\n"},
		},
	}
	result, err := rt.toolChangeExecute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "REVISION_REQUIRED" {
		t.Fatalf("missing revision must map to REVISION_REQUIRED, got %v: %+v", errorBody["code"], response)
	}
	details, _ := errorBody["details"].(map[string]any)
	nextAction, _ := details["next_action"].(map[string]any)
	if nextAction["tool"] != "file_read" {
		t.Fatalf("REVISION_REQUIRED must carry a file_read recovery action: %+v", details)
	}
	arguments, _ := nextAction["arguments"].(map[string]any)
	if arguments["remote_session_id"] != created.Session.ID {
		t.Fatalf("recovery action must preserve the session: %+v", arguments)
	}
}

func TestChangePrepareDuplicatePathHasRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"action":            "prepare",
		"summary":           "duplicate path",
		"operations": []map[string]any{
			{"operation": "create", "path": "a.txt", "content": "one\n"},
			{"operation": "create", "path": "a.txt", "content": "two\n"},
		},
	}
	result, err := rt.toolChangeManage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "PATCH_DUPLICATE_PATH" {
		t.Fatalf("unexpected error code: %v", errorBody["code"])
	}
	details, _ := errorBody["details"].(map[string]any)
	action, _ := details["next_action"].(map[string]any)
	if action["tool"] != "change_manage" {
		t.Fatalf("duplicate path must suggest change_manage: %+v", details)
	}
}

func TestChangedLineCountSmallEditInsideLargeFile(t *testing.T) {
	// A one-line edit inside a 1077-line file must count as a handful of
	// changed lines, not the whole file twice (1077*2+2 rejected real edits
	// against max_patch_lines=2000).
	lines := make([]string, 0, 1077)
	for i := 1; i <= 1077; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	original := []byte(strings.Join(lines, "\n") + "\n")
	lines[500] = "line 500 CHANGED"
	proposed := []byte(strings.Join(lines, "\n") + "\n")
	got := changedLineCount(original, proposed)
	if got > 10 {
		t.Fatalf("small edit counted as %d changed lines, want <= 10", got)
	}
	if got < 2 {
		t.Fatalf("small edit counted as %d changed lines, want >= 2", got)
	}
}

func TestChangedLineCountFullRewriteStaysLarge(t *testing.T) {
	original := []byte(strings.Repeat("old line\n", 1200))
	proposed := []byte(strings.Repeat("brand new content\n", 1200))
	got := changedLineCount(original, proposed)
	if got < 2000 {
		t.Fatalf("full rewrite counted as %d changed lines, want >= 2000", got)
	}
}

func TestChangedLineCountCreateAndDelete(t *testing.T) {
	content := []byte(strings.Repeat("line\n", 20))
	if got := changedLineCount(nil, content); got != 20 {
		t.Fatalf("create counted as %d lines, want 20", got)
	}
	if got := changedLineCount(content, nil); got != 20 {
		t.Fatalf("delete counted as %d lines, want 20", got)
	}
	if got := changedLineCount(content, content); got != 0 {
		t.Fatalf("identical content counted as %d lines, want 0", got)
	}
}
