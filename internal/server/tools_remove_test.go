package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
)

func callRemove(t *testing.T, handler mcp.ToolHandler, arguments map[string]any) map[string]any {
	t.Helper()
	if _, exists := arguments["intent"]; !exists {
		arguments["intent"] = "test workspace removal"
	}
	request := mcpresult.Request(arguments)
	result, err := handler(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return decodeToolResult(t, result)
}

func TestRemovePrepareCommitFreezesDirectoryAndRequiresWebConfirmationUUID(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	root := filepath.Join(workspace.Path, "remove-tree")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	prepareArgs := map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "remove the explicitly approved fixture tree",
		"idempotency_key": "remove-tree-1", "targets": []any{map[string]any{"path": "remove-tree", "kind": "directory"}},
	}
	prepared := callRemove(t, rt.toolWorkspaceDeletePrepare, prepareArgs)
	if !statusOK(prepared) {
		t.Fatalf("remove prepare failed: %+v", prepared)
	}
	data, _ := prepared["data"].(map[string]any)
	confirmationUUID, _ := data["confirmation_uuid"].(string)
	if len(confirmationUUID) != 36 || confirmationUUID[8] != '-' || confirmationUUID[13] != '-' || confirmationUUID[18] != '-' || confirmationUUID[23] != '-' {
		t.Fatalf("remove_prepare must return an RFC 4122 UUID: %q", confirmationUUID)
	}
	if summary := envelopeHumanSummary(envelope.Response{Status: envelope.StatusOK, Data: data}); !strings.Contains(summary, "confirmation_uuid: `"+confirmationUUID+"`") || !strings.Contains(summary, "submit_remove") {
		t.Fatalf("remove_prepare summary must expose the submit credential: %q", summary)
	}
	if data["filesystem_mutated"] != false || data["requires_user_confirmation"] != true || data["entry_count"] != float64(4) {
		t.Fatalf("remove prepare contract=%+v", data)
	}
	if data["idempotency_key"] != "remove-tree-1" {
		t.Fatalf("remove prepare must return idempotency_key=%q: %+v", "remove-tree-1", data)
	}
	submitArguments, _ := data["submit_remove_arguments"].(map[string]any)
	for key, want := range map[string]any{
		"delete_request_id": data["delete_request_id"],
		"manifest_sha256":   data["manifest_sha256"],
		"confirmation_uuid": data["confirmation_uuid"],
		"idempotency_key":   "remove-tree-1",
	} {
		if submitArguments[key] != want {
			t.Fatalf("submit_remove_arguments[%s]=%v, want %v; data=%+v", key, submitArguments[key], want, data)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("prepare changed directory tree: %v", err)
	}

	commitArgs := map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "remove the explicitly approved fixture tree",
		"delete_request_id": data["delete_request_id"], "manifest_sha256": data["manifest_sha256"],
		"idempotency_key": "remove-tree-1",
	}
	withoutUUID := callRemove(t, rt.toolWorkspaceDeleteCommit, cloneMap(commitArgs))
	if statusOK(withoutUUID) || errorCode(withoutUUID) != "confirmation_required" {
		t.Fatalf("commit without confirmation uuid=%+v", withoutUUID)
	}
	wrongUUID := cloneMap(commitArgs)
	wrongUUID["confirmation_uuid"] = "00000000-0000-4000-8000-000000000000"
	wrongConfirmation := callRemove(t, rt.toolWorkspaceDeleteCommit, wrongUUID)
	if statusOK(wrongConfirmation) || errorCode(wrongConfirmation) != "confirmation_mismatch" {
		t.Fatalf("wrong confirmation uuid=%+v", wrongConfirmation)
	}
	commitArgs["confirmation_uuid"] = confirmationUUID
	committed := callRemove(t, rt.toolWorkspaceDeleteCommit, cloneMap(commitArgs))
	if !statusOK(committed) {
		t.Fatalf("remove commit failed: %+v", committed)
	}
	commitData, _ := committed["data"].(map[string]any)
	if commitData["deleted_count"] != float64(4) || commitData["failed_count"] != float64(0) {
		t.Fatalf("remove commit counts=%+v", commitData)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("directory tree remains after commit: %v", err)
	}
	replay := callRemove(t, rt.toolWorkspaceDeleteCommit, cloneMap(commitArgs))
	if !statusOK(replay) || replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("remove replay=%+v", replay)
	}
}

func TestRemoveCommitRejectsStaleTreeWithoutMutation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	path := filepath.Join(workspace.Path, "stale-remove.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	prepared := callRemove(t, rt.toolWorkspaceDeletePrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "prepare stale removal", "idempotency_key": "stale-remove-1",
		"targets": []any{map[string]any{"path": "stale-remove.txt", "kind": "file", "expected_sha256": digestForTest([]byte("before\n"))}},
	})
	if !statusOK(prepared) {
		t.Fatalf("prepare stale removal=%+v", prepared)
	}
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := prepared["data"].(map[string]any)
	result := callRemove(t, rt.toolWorkspaceDeleteCommit, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "commit stale removal",
		"delete_request_id": data["delete_request_id"], "manifest_sha256": data["manifest_sha256"],
		"confirmation_uuid": prepared["data"].(map[string]any)["confirmation_uuid"], "idempotency_key": "stale-remove-1",
	})
	if !statusOK(result) {
		t.Fatalf("stale remove result=%+v", result)
	}
	resultData, _ := result["data"].(map[string]any)
	if resultData["status"] != "failed" || resultData["failed_count"] != float64(1) {
		t.Fatalf("stale remove must be a structured failed result: %+v", result)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "after\n" {
		t.Fatalf("stale commit mutated file: %q %v", content, err)
	}
}

func TestRemovePrepareRejectsEscapeAndSymlink(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	outside := filepath.Join(filepath.Dir(workspace.Path), "outside-remove.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.Symlink(outside, filepath.Join(workspace.Path, "link.txt")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(workspace.Path, "link.txt")) })
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	for _, target := range []map[string]any{
		{"path": "../outside-remove.txt", "kind": "file", "expected_sha256": digestForTest([]byte("outside\n"))},
		{"path": "link.txt", "kind": "file", "expected_sha256": digestForTest([]byte("outside\n"))},
	} {
		result := callRemove(t, rt.toolWorkspaceDeletePrepare, map[string]any{
			"remote_session_id": remoteID, "workspace": "demo", "purpose": "reject unsafe remove", "idempotency_key": "unsafe-" + target["path"].(string),
			"targets": []any{target},
		})
		if statusOK(result) {
			t.Fatalf("unsafe remove accepted: target=%+v result=%+v", target, result)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was affected: %v", err)
	}
}

func TestRemoveCommitDeletesDirectoryUsingServerChunks(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	root := filepath.Join(workspace.Path, "chunked-remove")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 65; index++ {
		path := filepath.Join(root, "f-"+fmt.Sprintf("%03d", index)+".txt")
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	prepared := callRemove(t, rt.toolWorkspaceDeletePrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "remove chunked fixture", "idempotency_key": "chunked-remove-1",
		"targets": []any{map[string]any{"path": "chunked-remove", "kind": "directory"}},
	})
	if !statusOK(prepared) {
		t.Fatalf("chunked prepare=%+v", prepared)
	}
	data := prepared["data"].(map[string]any)
	result := callRemove(t, rt.toolWorkspaceDeleteCommit, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "remove chunked fixture",
		"delete_request_id": data["delete_request_id"], "manifest_sha256": data["manifest_sha256"],
		"confirmation_uuid": prepared["data"].(map[string]any)["confirmation_uuid"], "idempotency_key": "chunked-remove-1",
	})
	if !statusOK(result) {
		t.Fatalf("chunked commit=%+v", result)
	}
	commitData := result["data"].(map[string]any)
	if commitData["deleted_count"] != float64(66) || commitData["failed_count"] != float64(0) {
		t.Fatalf("chunked commit counts=%+v", commitData)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("chunked directory remains: %v", err)
	}
}
