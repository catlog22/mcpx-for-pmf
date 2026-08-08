package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
	"mcpx/internal/observation"
)

func TestCleanCoreCatalogReplacesP0PublicNames(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	tools := runtime.listedToolMap()
	for _, name := range []string{"session", "read", "edit", "observe"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("clean-core tool %q is not registered", name)
		}
	}
	for _, name := range []string{"workspace_read", "session_read", "source_read", "change", "change_read"} {
		if _, ok := tools[name]; ok {
			t.Fatalf("legacy P0 tool %q is still registered", name)
		}
	}

	var sessionSchema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(tools["session"]), &sessionSchema); err != nil {
		t.Fatal(err)
	}
	sessionProperties := sessionSchema["properties"].(map[string]any)
	if sessionProperties["remote_session_id"] == nil {
		t.Fatalf("session must expose remote_session_id: %+v", sessionProperties)
	}
	if _, exists := sessionProperties["handoff_token"]; exists {
		t.Fatal("clean-core session must not expose handoff_token")
	}
	actions := sessionProperties["action"].(map[string]any)["enum"].([]any)
	if strings.Contains(strings.Join(anyStrings(actions), ","), "handoff") {
		t.Fatalf("session action enum contains handoff: %v", actions)
	}

	editTool := tools["edit"]
	if editTool.Annotations == nil || editTool.Annotations.DestructiveHint == nil || !*editTool.Annotations.DestructiveHint || !editTool.Annotations.IdempotentHint {
		t.Fatalf("edit annotations do not express destructive idempotent behavior: %+v", editTool.Annotations)
	}
}

func TestCleanCoreReadSupportsMixedFullAndWindowBatch(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace missing")
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "full.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "window.txt"), []byte("zero\none\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	response := callEnvelope(t, rt.toolRead, context.Background(), map[string]any{
		"remote_session_id": remoteID,
		"view":              "file",
		"items": []map[string]any{
			{"path": "full.txt", "mode": "full"},
			{"path": "window.txt", "mode": "window", "offset": 1, "limit": 1},
		},
	})
	if !statusOK(response) {
		t.Fatalf("read failed: %+v", response)
	}
	items := asMapSlice(response["data"].(map[string]any)["results"])
	if len(items) != 2 {
		t.Fatalf("read results=%+v", items)
	}
	if items[0]["mode"] != "full" || items[0]["content"] != "one\ntwo\n" {
		t.Fatalf("full item=%+v", items[0])
	}
	if items[1]["mode"] != "window" || items[1]["content"] != "one\n" {
		t.Fatalf("window item=%+v", items[1])
	}
}

func TestCleanCoreSessionAttachReusesOnlySuppliedID(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	missing := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{
		"action": "attach", "workspace": "demo",
	})
	if statusOK(missing) || errorCode(missing) != "remote_session_required" {
		t.Fatalf("attach without remote id=%+v", missing)
	}

	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	attached := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{
		"action": "attach", "remote_session_id": remoteID,
	})
	if attached["remote_session_id"] != remoteID {
		t.Fatalf("attach changed remote id: %+v", attached)
	}
}

func TestCleanCoreEditAppliesIdempotentlyAndReportsStale(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	path := filepath.Join(workspace.Path, "edit.txt")
	original := []byte("title: old\ncolor: red\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	request := map[string]any{
		"remote_session_id": remoteID,
		"purpose":           "update the file labels",
		"idempotency_key":   "edit-test-1",
		"edits": []map[string]any{{
			"path":        "edit.txt",
			"operation":   "update",
			"base_sha256": digestForTest(original),
			"replacements": []map[string]any{
				{"match": "title: old", "replacement": "title: new"},
				{"match": "color: red", "replacement": "color: blue"},
			},
		}},
	}
	first := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if !statusOK(first) {
		t.Fatalf("edit failed: %+v", first)
	}
	data := first["data"].(map[string]any)
	if data["total_changed_lines"] != float64(4) && data["total_changed_lines"] != 4 {
		t.Fatalf("edit changed lines=%v", data["total_changed_lines"])
	}
	if diff, _ := data["diff_summary"].(string); !strings.Contains(diff, "+title: new") {
		t.Fatalf("diff summary=%q", diff)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "title: new\ncolor: blue\n" {
		t.Fatalf("edited content=%q", content)
	}

	replay := callEnvelope(t, rt.toolEdit, context.Background(), request)
	replayData := replay["data"].(map[string]any)
	if replayData["idempotent_replay"] != true {
		t.Fatalf("replay did not use idempotency cache: %+v", replayData)
	}

	stale := callEnvelope(t, rt.toolEdit, context.Background(), map[string]any{
		"remote_session_id": remoteID,
		"purpose":           "test stale revision",
		"edits": []map[string]any{{
			"path": "edit.txt", "operation": "update", "base_sha256": "sha256:stale",
			"replacements": []map[string]any{{"match": "title: new", "replacement": "title: newest"}},
		}},
	})
	if statusOK(stale) || errorCode(stale) != "stale_revision" {
		t.Fatalf("stale response=%+v", stale)
	}
	if details, _ := stale["error"].(map[string]any)["details"].(map[string]any); details["current_sha256"] == nil {
		t.Fatalf("stale response missing current sha: %+v", stale)
	}
}

func TestCleanCoreObserveChangesReturnsInlineDiff(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	payload, err := json.Marshal(map[string]any{
		"diff_summary":        "--- a/a.txt\n+++ b/a.txt\n-old\n+new\n",
		"total_changed_lines": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.observation.Record(context.Background(), observation.Event{
		Workspace: "demo", RemoteSessionID: remoteID, Tool: "edit", Type: observation.TypeFileChanged,
		Status: "succeeded", Path: "a.txt", Output: payload,
	}); err != nil {
		t.Fatal(err)
	}
	response := callEnvelope(t, rt.toolObserve, context.Background(), map[string]any{
		"remote_session_id": remoteID, "view": "changes",
	})
	if !statusOK(response) {
		t.Fatalf("observe failed: %+v", response)
	}
	changes := asMapSlice(response["data"].(map[string]any)["changes"])
	if len(changes) != 1 || !strings.Contains(changes[0]["diff_summary"].(string), "+new") {
		t.Fatalf("observe changes=%+v", changes)
	}
}

func TestCleanCoreEditBoundsLargeDiffAndPaginatesFullDiff(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	oldText := strings.Repeat("old-", 200) + "\n"
	newText := strings.Repeat("new-", 200) + "\n"
	old := strings.Repeat(oldText, 400)
	updated := strings.Repeat(newText, 400)
	path := filepath.Join(workspace.Path, "large.txt")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	response := callEnvelope(t, rt.toolEdit, context.Background(), map[string]any{
		"remote_session_id": remoteID,
		"purpose":           "replace a large generated block",
		"idempotency_key":   "large-diff-1",
		"edits": []map[string]any{{
			"path": "large.txt", "operation": "update", "base_sha256": digestForTest([]byte(old)), "content": updated,
		}},
	})
	if !statusOK(response) {
		t.Fatalf("large edit failed: %+v", response)
	}
	data := response["data"].(map[string]any)
	preview, _ := data["diff_summary"].(string)
	if data["diff_truncated"] != true || len(preview) > cleanDiffTotalPreviewMaxBytes || data["edit_id"] == "" {
		t.Fatalf("large diff was not bounded: bytes=%d data=%+v", len(preview), data)
	}
	if diffBytes, _ := data["diff_bytes"].(float64); diffBytes <= float64(cleanDiffTotalPreviewMaxBytes) {
		t.Fatalf("large diff byte count=%v", data["diff_bytes"])
	}
	editID := data["edit_id"].(string)
	page := callEnvelope(t, rt.toolObserve, context.Background(), map[string]any{
		"remote_session_id": remoteID, "view": "diff", "edit_id": editID, "offset": 0, "limit": 1024,
	})
	if !statusOK(page) {
		t.Fatalf("diff page failed: %+v", page)
	}
	pageData := page["data"].(map[string]any)
	if pageData["eof"] == true || pageData["next_offset"] == nil || pageData["diff"] == "" {
		t.Fatalf("diff page did not expose continuation: %+v", pageData)
	}
}

func TestCleanCoreEditRejectsIdempotencyFingerprintConflict(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	path := filepath.Join(workspace.Path, "conflict.txt")
	original := []byte("old\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	base := digestForTest(original)
	first := map[string]any{
		"remote_session_id": remoteID, "purpose": "first", "idempotency_key": "same-key",
		"edits": []map[string]any{{"path": "conflict.txt", "operation": "update", "base_sha256": base, "replacements": []map[string]any{{"match": "old", "replacement": "new"}}}},
	}
	if response := callEnvelope(t, rt.toolEdit, context.Background(), first); !statusOK(response) {
		t.Fatalf("first edit failed: %+v", response)
	}
	conflict := map[string]any{
		"remote_session_id": remoteID, "purpose": "different", "idempotency_key": "same-key",
		"edits": []map[string]any{{"path": "conflict.txt", "operation": "update", "base_sha256": base, "replacements": []map[string]any{{"match": "old", "replacement": "other"}}}},
	}
	response := callEnvelope(t, rt.toolEdit, context.Background(), conflict)
	if statusOK(response) || errorCode(response) != "idempotency_conflict" {
		t.Fatalf("expected idempotency conflict: %+v", response)
	}
}

func TestCleanCoreUTF16ReadEditRoundTripPreservesRawBytes(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	logical := "第一行\r\nemoji 😀\r\n第三行\r\n"
	original := append([]byte{0xff, 0xfe}, encodeUTF16ForServerTest(logical, binary.LittleEndian)...)
	path := filepath.Join(workspace.Path, "utf16-edit.txt")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	read := callEnvelope(t, rt.toolRead, context.Background(), map[string]any{
		"remote_session_id": remoteID, "view": "file", "path": "utf16-edit.txt", "mode": "full",
	})
	if !statusOK(read) {
		t.Fatalf("UTF-16 read failed: %+v", read)
	}
	readData := read["data"].(map[string]any)
	item := readData
	if item["content"] != logical || item["encoding"] != "utf-8" {
		t.Fatalf("decoded UTF-16 payload=%+v", item)
	}
	baseSHA, _ := item["sha256"].(string)
	response := callEnvelope(t, rt.toolEdit, context.Background(), map[string]any{
		"remote_session_id": remoteID, "purpose": "update UTF-16 text", "idempotency_key": "utf16-roundtrip-1",
		"edits": []map[string]any{{
			"path": "utf16-edit.txt", "operation": "update", "base_sha256": baseSHA,
			"replacements": []map[string]any{{"match": "第三行", "replacement": "第三行-已改"}},
		}},
	})
	if !statusOK(response) {
		t.Fatalf("UTF-16 edit failed: %+v", response)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xff, 0xfe}, encodeUTF16ForServerTest("第一行\r\nemoji 😀\r\n第三行-已改\r\n", binary.LittleEndian)...)
	if string(updated) != string(want) {
		t.Fatalf("UTF-16 raw bytes changed unexpectedly: got %x want %x", updated, want)
	}
	if digestForTest(updated) == baseSHA {
		t.Fatal("UTF-16 edit did not change raw SHA")
	}
}

func encodeUTF16ForServerTest(text string, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(text))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		order.PutUint16(encoded[index*2:], unit)
	}
	return encoded
}

func digestForTest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func anyStrings(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
