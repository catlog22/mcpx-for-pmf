package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"action":            "submit",
		"remote_session_id": remoteID,
		"confirmation_uuid": data["confirmation_uuid"],
	}
}

func writeMoveOutTestCommand(t *testing.T, directory, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func installMoveOutTrashMocks(t *testing.T, platform string) string {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("POSIX command mocks are only used to exercise all platform branches on Unix CI")
	}
	binDir := t.TempDir()
	trashDir := t.TempDir()
	var uname string
	var command string
	switch platform {
	case "windows":
		uname = "MINGW64_NT-10.0"
		command = "powershell.exe"
	case "darwin":
		uname = "Darwin"
		command = "osascript"
	case "linux":
		uname = "Linux"
		command = "gio"
	default:
		t.Fatalf("unsupported test platform %q", platform)
	}
	writeMoveOutTestCommand(t, binDir, "uname", "printf '%s\\n' '"+uname+"'")
	writeMoveOutTestCommand(t, binDir, command, `last=''
for arg in "$@"; do last=$arg; done
name=${last##*/}
mv "$last" "$MCPX_TEST_TRASH/$name"`)
	t.Setenv("MCPX_TEST_TRASH", trashDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return trashDir
}

func TestMoveOutPrepareAndCommitAtomicallyMovesDirectoryWithoutScanningDescendants(t *testing.T) {
	trashDir := installMoveOutTrashMocks(t, "linux")
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
	prepared := callMoveOut(t, rt.toolMoveOut, map[string]any{
		"action":            "prepare",
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
	expiresAt, err := time.Parse(time.RFC3339Nano, data["expires_at"].(string))
	if err != nil {
		t.Fatalf("prepare expires_at is not RFC3339: %+v", data["expires_at"])
	}
	remaining := time.Until(expiresAt)
	if remaining < 29*time.Minute || remaining > 31*time.Minute {
		t.Fatalf("prepare expires_at must expose the active confirmation TTL, remaining=%s expires_at=%s", remaining, expiresAt)
	}
	if _, exists := data["entries"]; exists {
		t.Fatalf("prepare returned directory descendants: %+v", data)
	}
	confirmationUUID := data["confirmation_uuid"].(string)
	if len(confirmationUUID) != 36 || !strings.Contains(envelopeHumanSummary(envelope.Response{Status: envelope.StatusOK, Data: data}), "move_out(action=submit)") {
		t.Fatalf("prepare does not expose web confirmation credential: %+v", data)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("prepare mutated source directory: %v", err)
	}
	nextAction, _ := data["next_action"].(map[string]any)
	submitArguments, _ := nextAction["arguments"].(map[string]any)
	if nextAction["tool"] != "move_out" || len(submitArguments) != 3 || submitArguments["action"] != "submit" || submitArguments["remote_session_id"] != remoteID || submitArguments["confirmation_uuid"] != confirmationUUID {
		t.Fatalf("prepare must return the exact move_out submit action: %+v", nextAction)
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
	if result := callMoveOut(t, rt.toolMoveOut, withoutConfirmation); statusOK(result) || errorCode(result) != "confirmation_required" {
		t.Fatalf("commit without confirmation=%+v", result)
	}
	committed := callMoveOut(t, rt.toolMoveOut, moveOutCommitArguments(remoteID, data))
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
	trashPath := preview["quarantine_path"].(string)
	if !strings.HasPrefix(trashPath, "trash://") {
		t.Fatalf("unexpected trash path: %q", trashPath)
	}
	if content, err := os.ReadFile(filepath.Join(trashDir, "move-tree", "nested", "content.txt")); err != nil || string(content) != "content\n" {
		t.Fatalf("directory content was not moved intact: %q %v", content, err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "outside\n" {
		t.Fatalf("move followed external symlink: %q %v", content, err)
	}
	replay := callMoveOut(t, rt.toolMoveOut, moveOutCommitArguments(remoteID, data))
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

func TestCommitMoveOutManifestUsesSystemTrashAcrossPlatforms(t *testing.T) {
	for _, testCase := range []struct {
		platform       string
		locationPrefix string
	}{
		{platform: "windows", locationPrefix: "recycle-bin://"},
		{platform: "darwin", locationPrefix: "trash://"},
		{platform: "linux", locationPrefix: "trash://"},
	} {
		t.Run(testCase.platform, func(t *testing.T) {
			trashDir := installMoveOutTrashMocks(t, testCase.platform)
			rt := newWorkspaceRuntime(t, "demo")
			workspace, _ := rt.reg.Get("demo")
			name := "platform-trash-" + testCase.platform + ".txt"
			content := []byte(testCase.platform + "\n")
			path := filepath.Join(workspace.Path, name)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			remoteID := openMoveOutSession(t, rt)
			prepared := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
				"remote_session_id": remoteID, "workspace": "demo", "purpose": "将平台兼容性测试文件移入系统回收站", "idempotency_key": "platform-trash-" + testCase.platform,
				"targets": []any{map[string]any{"path": name, "kind": "file", "expected_sha256": digestForTest(content)}},
			})
			if !statusOK(prepared) {
				t.Fatalf("prepare %s=%+v", testCase.platform, prepared)
			}
			committed := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, prepared["data"].(map[string]any)))
			if !statusOK(committed) {
				t.Fatalf("commit %s=%+v", testCase.platform, committed)
			}
			commitData := committed["data"].(map[string]any)
			if commitData["moved_count"] != float64(1) || commitData["failed_count"] != float64(0) {
				t.Fatalf("platform move result %s=%+v", testCase.platform, commitData)
			}
			if location, _ := commitData["quarantine_location"].(string); !strings.HasPrefix(location, testCase.locationPrefix) {
				t.Fatalf("platform trash location %s=%q", testCase.platform, location)
			}
			preview := commitData["target_preview"].([]any)[0].(map[string]any)
			if trashPath, _ := preview["quarantine_path"].(string); !strings.HasPrefix(trashPath, testCase.locationPrefix) {
				t.Fatalf("platform trash path %s=%q", testCase.platform, trashPath)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("source remains after %s trash move: %v", testCase.platform, err)
			}
			if actual, err := os.ReadFile(filepath.Join(trashDir, name)); err != nil || string(actual) != string(content) {
				t.Fatalf("mock system trash did not receive %s file: %q %v", testCase.platform, actual, err)
			}
		})
	}
}

func TestCommitMoveOutManifestLinuxFallbackWritesFreeDesktopTrashInfo(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("FreeDesktop trash fallback is Unix-only")
	}
	binDir := t.TempDir()
	writeMoveOutTestCommand(t, binDir, "uname", "printf '%s\\n' 'Linux'")
	t.Setenv("PATH", binDir)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	name := "fallback trash %.txt"
	content := []byte("fallback\n")
	path := filepath.Join(workspace.Path, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	remoteID := openMoveOutSession(t, rt)
	prepared := callMoveOut(t, rt.toolWorkspaceMoveOutPrepare, map[string]any{
		"remote_session_id": remoteID, "workspace": "demo", "purpose": "验证 Linux FreeDesktop 回收站 fallback", "idempotency_key": "linux-trash-fallback",
		"targets": []any{map[string]any{"path": name, "kind": "file", "expected_sha256": digestForTest(content)}},
	})
	if !statusOK(prepared) {
		t.Fatalf("linux fallback prepare=%+v", prepared)
	}
	committed := callMoveOut(t, rt.toolWorkspaceMoveOutCommit, moveOutCommitArguments(remoteID, prepared["data"].(map[string]any)))
	if !statusOK(committed) {
		t.Fatalf("linux fallback commit=%+v", committed)
	}
	commitData := committed["data"].(map[string]any)
	preview := commitData["target_preview"].([]any)[0].(map[string]any)
	trashPath := filepath.FromSlash(preview["quarantine_path"].(string))
	trashRoot := filepath.Join(dataHome, "Trash")
	if !strings.HasPrefix(trashPath, trashRoot+string(os.PathSeparator)) {
		t.Fatalf("fallback path is outside XDG trash: %q", trashPath)
	}
	if actual, err := os.ReadFile(trashPath); err != nil || string(actual) != string(content) {
		t.Fatalf("fallback trash file=%q %v", actual, err)
	}
	infoPath := filepath.Join(trashRoot, "info", filepath.Base(trashPath)+".trashinfo")
	info, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	infoText := string(info)
	if !strings.HasPrefix(infoText, "[Trash Info]\nPath=") || !strings.Contains(infoText, "%20") || !strings.Contains(infoText, "%25") {
		t.Fatalf("invalid FreeDesktop trash info: %q", infoText)
	}
	var deletionDate string
	for _, line := range strings.Split(infoText, "\n") {
		if strings.HasPrefix(line, "DeletionDate=") {
			deletionDate = strings.TrimPrefix(line, "DeletionDate=")
			break
		}
	}
	if _, err := time.Parse("2006-01-02T15:04:05", deletionDate); err != nil {
		t.Fatalf("invalid FreeDesktop DeletionDate %q: %v", deletionDate, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("source remains after FreeDesktop fallback: %v", err)
	}
}

func TestMoveOutMovesSymlinkEntryWithoutFollowingTarget(t *testing.T) {
	trashDir := installMoveOutTrashMocks(t, "linux")
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
	trashPath := committed["data"].(map[string]any)["target_preview"].([]any)[0].(map[string]any)["quarantine_path"].(string)
	if !strings.HasPrefix(trashPath, "trash://") {
		t.Fatalf("unexpected symlink trash path: %q", trashPath)
	}
	if info, err := os.Lstat(filepath.Join(trashDir, "node_modules-link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("trash entry is not the moved symlink: %v", err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "preserve\n" {
		t.Fatalf("symlink move touched external target: %q %v", content, err)
	}
}
