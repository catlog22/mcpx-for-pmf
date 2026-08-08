package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
)

func callMoveOut(t *testing.T, handler mcp.ToolHandler, arguments map[string]any) map[string]any {
	t.Helper()
	if _, exists := arguments["intent"]; !exists {
		arguments["intent"] = "test workspace safe move-out"
	}
	result, err := handler(context.Background(), mcpresult.Request(arguments))
	if err != nil {
		t.Fatal(err)
	}
	return decodeToolResult(t, result)
}

func openMoveOutSession(t *testing.T, rt *Runtime) string {
	t.Helper()
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	return opened["remote_session_id"].(string)
}

func moveOutCommitArguments(remoteID string, data map[string]any) map[string]any {
	return map[string]any{
		"remote_session_id": remoteID,
		"confirmation_uuid": data["confirmation_uuid"],
	}
}

func TestMoveOutPrepareAndCommitAtomicallyMovesDirectoryWithoutScanningDescendants(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	directory := filepath.Join(workspace.Path, "move-tree")
	outside := filepath.Join(t.TempDir(), "outside-target.txt")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "content.txt"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "nested", "external-link")); err != nil {
		t.Fatal(err)
	}
	remoteID := openMoveOutSession(t, rt)
	prepared := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID,
		"workspace":         "demo",
		"purpose":           "将用户授权的压测目录安全移至隔离区",
		"idempotency_key":   "move-tree-1",
		"targets":           []any{map[string]any{"path": "move-tree", "kind": "directory"}},
	})
	if !statusOK(prepared) {
		t.Fatalf("move-out prepare failed: %+v", prepared)
	}
	data := prepared["data"].(map[string]any)
	if data["filesystem_mutated"] != false || data["directory_contents_enumerated"] != false || data["target_count"] != float64(1) || data["total_bytes_known"] != false {
		t.Fatalf("prepare must be bounded and non-destructive: %+v", data)
	}
	if _, exists := data["entries"]; exists {
		t.Fatalf("prepare returned directory descendants: %+v", data)
	}
	confirmationUUID := data["confirmation_uuid"].(string)
	if len(confirmationUUID) != 36 || !strings.Contains(envelopeHumanSummary(envelope.Response{Status: envelope.StatusOK, Data: data}), "submit_move_out") {
		t.Fatalf("prepare does not expose web confirmation credential: %+v", data)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("prepare mutated source directory: %v", err)
	}
	submitArguments, _ := data["submit_move_out_arguments"].(map[string]any)
	if len(submitArguments) != 2 || submitArguments["remote_session_id"] != remoteID || submitArguments["confirmation_uuid"] != confirmationUUID {
		t.Fatalf("prepare must return only the two commit arguments: %+v", submitArguments)
	}
	purposeConflict := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID,
		"workspace":         "demo",
		"purpose":           "将不同范围的目录安全移至隔离区",
		"idempotency_key":   "move-tree-1",
		"targets":           []any{map[string]any{"path": "move-tree", "kind": "directory"}},
	})
	if statusOK(purposeConflict) || errorCode(purposeConflict) != "idempotency_conflict" {
		t.Fatalf("idempotency key accepted a different purpose: %+v", purposeConflict)
	}

	withoutConfirmation := moveOutCommitArguments(remoteID, data)
	delete(withoutConfirmation, "confirmation_uuid")
	if result := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, withoutConfirmation); statusOK(result) || errorCode(result) != "confirmation_required" {
		t.Fatalf("commit without confirmation=%+v", result)
	}
	committed := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, data))
	if !statusOK(committed) {
		t.Fatalf("move-out commit failed: %+v", committed)
	}
	commitData := committed["data"].(map[string]any)
	if commitData["moved_count"] != float64(1) || commitData["moved_bytes_known"] != false || commitData["reversible"] != true {
		t.Fatalf("unexpected move result: %+v", commitData)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("directory remains inside workspace after move: %v", err)
	}
	preview := commitData["target_preview"].([]any)[0].(map[string]any)
	quarantinePath := preview["quarantine_path"].(string)
	if !strings.HasPrefix(filepath.ToSlash(quarantinePath), ".mcpx-quarantine/") {
		t.Fatalf("unexpected quarantine path: %q", quarantinePath)
	}
	quarantineAbsolutePath := filepath.Join(filepath.Dir(workspace.Path), quarantinePath)
	if content, err := os.ReadFile(filepath.Join(quarantineAbsolutePath, "nested", "content.txt")); err != nil || string(content) != "content\n" {
		t.Fatalf("directory content was not moved intact: %q %v", content, err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "outside\n" {
		t.Fatalf("move followed external symlink: %q %v", content, err)
	}
	replay := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, data))
	if !statusOK(replay) || replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("exact commit replay=%+v", replay)
	}
}

func TestMoveOutRejectsStalePreviewAndPathEscapeWithoutMutation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	path := filepath.Join(workspace.Path, "stale-move.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteID := openMoveOutSession(t, rt)
	prepared := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "将临时文件安全移至隔离区", "idempotency_key": "stale-move-1",
		"targets": []any{map[string]any{"path": "stale-move.txt", "kind": "file", "expected_sha256": digestForTest([]byte("before\n"))}},
	})
	if !statusOK(prepared) {
		t.Fatalf("prepare stale fixture=%+v", prepared)
	}
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := prepared["data"].(map[string]any)
	committed := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, data))
	if !statusOK(committed) || committed["data"].(map[string]any)["status"] != "failed" {
		t.Fatalf("stale move result=%+v", committed)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "after\n" {
		t.Fatalf("stale move mutated source: %q %v", content, err)
	}
	preview := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "仅预览安全移出清单，不执行移动", "idempotency_key": "preview-only-move-1",
		"targets": []any{map[string]any{"path": "stale-move.txt", "kind": "file", "expected_sha256": digestForTest([]byte("after\n"))}},
	})
	previewData := preview["data"].(map[string]any)
	if result := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, previewData)); statusOK(result) || errorCode(result) != "move_out_purpose_mismatch" {
		t.Fatalf("preview-only move was accepted: %+v", result)
	}
	if result := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "reject unsafe move-out", "idempotency_key": "escape-move-1",
		"targets": []any{map[string]any{"path": "../outside", "kind": "file", "expected_sha256": digestForTest([]byte("x"))}},
	}); statusOK(result) {
		t.Fatalf("path escape was accepted: %+v", result)
	}
}

func TestMoveOutMovesSymlinkEntryWithoutFollowingTarget(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	outside := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(outside, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace.Path, "node_modules-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	remoteID := openMoveOutSession(t, rt)
	prepared := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "将用户授权的 node_modules 链接安全移至隔离区", "idempotency_key": "move-symlink-1",
		"targets": []any{map[string]any{"path": "node_modules-link", "kind": "symlink"}},
	})
	data := prepared["data"].(map[string]any)
	committed := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, data))
	if !statusOK(committed) || committed["data"].(map[string]any)["moved_count"] != float64(1) {
		t.Fatalf("symlink move=%+v", committed)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("symlink remains in workspace: %v", err)
	}
	quarantinePath := committed["data"].(map[string]any)["target_preview"].([]any)[0].(map[string]any)["quarantine_path"].(string)
	if info, err := os.Lstat(filepath.Join(filepath.Dir(workspace.Path), quarantinePath)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("quarantine entry is not the moved symlink: %v", err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "preserve\n" {
		t.Fatalf("symlink move touched external target: %q %v", content, err)
	}
}
