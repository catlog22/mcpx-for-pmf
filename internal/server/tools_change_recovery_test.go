package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/changeset"
	"mcpx/internal/remotesession"
)

func TestChangesetDigestIsVisibleInPrepareAndReadToolText(t *testing.T) {
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
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent":            "prepare a visible digest test",
		"remote_session_id": created.Session.ID,
		"summary":           "visible digest",
		"operations": []any{map[string]any{
			"operation": "create", "path": "visible-digest.txt", "content": "digest\n",
		}},
	}
	prepared, err := rt.toolHandlers["change_prepare"](context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	preparedText := prepared.Content[0].(mcp.TextContent).Text
	preparedEnvelope := decodeARCEnvelope(t, prepared)
	preparedData := preparedEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	digest, _ := preparedData["digest"].(string)
	if digest == "" || preparedData["expected_digest"] != digest {
		t.Fatalf("prepared digest fields=%+v", preparedData)
	}
	if !strings.Contains(preparedText, "`expected_digest`: `"+digest+"`") || !strings.Contains(preparedText, "必须原样复制") {
		t.Fatalf("model-facing prepare text missing exact digest: %s", preparedText)
	}

	changesetID, _ := preparedData["changeset_id"].(string)
	readRequest := mcp.CallToolRequest{}
	readRequest.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID,
		"view":              "diff",
		"changeset_id":      changesetID,
	}
	read, err := rt.toolHandlers["change_read"](context.Background(), readRequest)
	if err != nil {
		t.Fatal(err)
	}
	readText := read.Content[0].(mcp.TextContent).Text
	readEnvelope := decodeARCEnvelope(t, read)
	readData := readEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	if readData["digest"] != digest || readData["expected_digest"] != digest {
		t.Fatalf("read digest fields=%+v want=%q", readData, digest)
	}
	if !strings.Contains(readText, "`expected_digest`: `"+digest+"`") {
		t.Fatalf("model-facing read text missing exact digest: %s", readText)
	}

	encoded, err := json.Marshal(preparedData)
	if err != nil || !strings.Contains(string(encoded), digest) {
		t.Fatalf("structured prepare payload lost digest: err=%v payload=%s", err, encoded)
	}

	conflictRequest := mcp.CallToolRequest{}
	conflictRequest.Params.Arguments = map[string]any{
		"intent":            "verify digest recovery visibility",
		"remote_session_id": created.Session.ID,
		"changeset_id":      changesetID,
		"expected_digest":   "sha256:wrong",
	}
	conflict, err := rt.toolHandlers["change_apply"](context.Background(), conflictRequest)
	if err != nil {
		t.Fatal(err)
	}
	conflictText := conflict.Content[0].(mcp.TextContent).Text
	if !strings.Contains(conflictText, digest) {
		t.Fatalf("model-facing conflict text missing exact expected digest %q: %s", digest, conflictText)
	}
}

func TestPublicChangePrepareCanApplyInOneCall(t *testing.T) {
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
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent":            "apply a one-call change",
		"remote_session_id": created.Session.ID,
		"summary":           "one-call change",
		"apply":             true,
		"operations": []any{map[string]any{
			"operation": "create", "path": "one-call.txt", "content": "applied\n",
		}},
	}
	result, err := rt.toolHandlers["change_prepare"](context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeARCEnvelope(t, result)
	data := envelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	if data["applied"] != true {
		t.Fatalf("one-call change data = %+v", data)
	}
	if _, err := os.Stat(filepath.Join(registered.Path, "one-call.txt")); err != nil {
		t.Fatalf("one-call change was not applied: %v", err)
	}
}

func TestChangeHistoryExposesDraftRecoveryActions(t *testing.T) {
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
	prepare := mcp.CallToolRequest{}
	prepare.Params.Arguments = map[string]any{
		"intent":            "prepare a discardable change",
		"remote_session_id": created.Session.ID,
		"summary":           "discardable change",
		"operations": []any{map[string]any{
			"operation": "create", "path": "discardable.txt", "content": "draft\n",
		}},
	}
	prepared, err := rt.toolHandlers["change_prepare"](context.Background(), prepare)
	if err != nil {
		t.Fatal(err)
	}
	preparedEnvelope := decodeARCEnvelope(t, prepared)
	preparedData := preparedEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	changesetID, _ := preparedData["changeset_id"].(string)
	if changesetID == "" {
		t.Fatalf("prepared data = %+v", preparedData)
	}

	history := mcp.CallToolRequest{}
	history.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID, "view": "history", "limit": 5,
	}
	historyResult, err := rt.toolHandlers["change_read"](context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	historyEnvelope := decodeARCEnvelope(t, historyResult)
	historyData := historyEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	summary := historyData["summary"].(map[string]any)
	if summary["draft_count"] != float64(1) {
		t.Fatalf("history summary = %+v", summary)
	}
	drafts := summary["active_drafts"].([]any)
	actions := drafts[0].(map[string]any)["next_actions"].([]any)
	if actions[0].(map[string]any)["tool"] != "change_apply" || actions[1].(map[string]any)["tool"] != "change_discard" {
		t.Fatalf("draft actions = %+v", actions)
	}
	historyDigest, _ := historyData["history_digest"].(string)
	if historyDigest == "" {
		t.Fatalf("history digest missing: %+v", historyData)
	}
	unchanged := mcp.CallToolRequest{}
	unchanged.Params.Arguments = map[string]any{
		"remote_session_id": created.Session.ID, "view": "history", "limit": 5,
		"known_history_digest": historyDigest,
	}
	unchangedResult, err := rt.toolHandlers["change_read"](context.Background(), unchanged)
	if err != nil {
		t.Fatal(err)
	}
	unchangedEnvelope := decodeARCEnvelope(t, unchangedResult)
	unchangedData := unchangedEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	if unchangedData["not_modified"] != true || len(unchangedData["changesets"].([]any)) != 0 {
		t.Fatalf("unchanged history response = %+v", unchangedData)
	}

	discard := mcp.CallToolRequest{}
	discard.Params.Arguments = map[string]any{
		"intent":            "discard the obsolete draft",
		"remote_session_id": created.Session.ID,
		"changeset_id":      changesetID,
	}
	if _, err := rt.toolHandlers["change_discard"](context.Background(), discard); err != nil {
		t.Fatal(err)
	}
	historyResult, err = rt.toolHandlers["change_read"](context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	historyEnvelope = decodeARCEnvelope(t, historyResult)
	historyData = historyEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)["data"].(map[string]any)
	summary = historyData["summary"].(map[string]any)
	if summary["draft_count"] != float64(0) {
		t.Fatalf("history after discard = %+v", summary)
	}
}

func TestPatchConflictExplainsLineEndingRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	content := []byte("class Demo {\r\n    int value = 1;\r\n}\r\n")
	if err := os.WriteFile(filepath.Join(registered.Path, "Demo.java"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := sha256.Sum256(content)
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent":            "verify line ending patch recovery",
		"remote_session_id": created.Session.ID,
		"summary":           "overlapping Java patch",
		"operations": []any{map[string]any{
			"operation": "update", "path": "Demo.java", "base_sha256": fmt.Sprintf("sha256:%x", base[:]),
			"patch": "@@ -1,3 +1,3 @@\n-class Demo {\n-    int value = 1;\n-}\n+class Demo {\n+    int value = 2;\n+}\n@@ -2,2 +2,2 @@\n-    int value = 1;\n+    int value = 2;\n }\n",
		}},
	}
	result, err := rt.toolHandlers["change_prepare"](context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"line_ending", "保留目标文件原有换行格式", "replace_exact", "base_sha256"} {
		if !strings.Contains(text, want) {
			t.Fatalf("patch recovery text missing %q: %s", want, text)
		}
	}
	arcEnvelope := decodeARCEnvelope(t, result)
	resultData := arcEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)
	data, _ := resultData["data"].(map[string]any)
	errorBody, _ := data["error"].(map[string]any)
	if errorBody["code"] != "PATCH_HUNKS_OVERLAP" {
		t.Fatalf("unexpected structured patch error: %+v", errorBody)
	}
	details, _ := errorBody["details"].(map[string]any)
	if !strings.Contains(fmt.Sprint(details["patch_guidance"]), "保留目标文件原有换行格式") {
		t.Fatalf("structured patch guidance missing: %+v", details)
	}
}

func TestChangeExecuteDoesNotWriteDiffFileToWorkspace(t *testing.T) {
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
		"intent":            "apply a file change",
		"remote_session_id": created.Session.ID,
		"summary":           "return diff without workspace artifact",
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
	if _, exists := data["diff_file"]; exists {
		t.Fatalf("change result must not expose a workspace diff file: %+v", data)
	}
	if _, err := os.Stat(filepath.Join(registered.Path, ".mcpx")); !os.IsNotExist(err) {
		t.Fatalf("change execution must not create workspace .mcpx artifacts: %v", err)
	}
}

func TestChangeExecuteDeletesNonGitFileAfterSemanticConfirmation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	path := filepath.Join(registered.Path, "remove-me.txt")
	if err := os.WriteFile(path, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	deleteRequest := mcp.CallToolRequest{}
	deleteRequest.Params.Arguments = map[string]any{
		"intent":            "delete the confirmed test file",
		"remote_session_id": created.Session.ID, "summary": "remove test file",
		"operations": []map[string]any{{"operation": "delete", "path": "remove-me.txt"}},
	}
	deleteResult, err := rt.toolChangeExecute(context.Background(), deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	pending := decodeToolResult(t, deleteResult)
	if pending["status"] != "waiting_confirmation" {
		t.Fatalf("delete should wait for semantic confirmation: %+v", pending)
	}
	pendingData, _ := pending["data"].(map[string]any)
	if pendingData["confirmation_required"] != true || pendingData["approval_id"] != nil {
		t.Fatalf("delete confirmation should be semantic and not expose a separate action: %+v", pendingData)
	}

	confirmRequest := mcp.CallToolRequest{}
	confirmRequest.Params.Arguments = map[string]any{
		"intent":            "用户已确认删除该文件",
		"remote_session_id": created.Session.ID, "changeset_id": pendingData["changeset_id"],
		"expected_digest": pendingData["digest"], "confirmation_token": pendingData["confirmation_token"],
	}
	confirmedResult, err := rt.toolChangeExecute(context.Background(), confirmRequest)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := decodeToolResult(t, confirmedResult)
	if confirmed["status"] != "ok" {
		t.Fatalf("confirmed delete failed in non-Git workspace: %+v", confirmed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("confirmed delete left the file behind: %v", err)
	}
	if pending := rt.approvals.ListRemoteSession(created.Session.ID); len(pending) != 0 {
		t.Fatalf("semantic confirmation should consume the pending request: %+v", pending)
	}
}

func TestChangeExecuteDeletesDirectoryWithInitialSemanticConfirmation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	directory := filepath.Join(registered.Path, "old-project")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent":            "清空旧项目目录",
		"remote_session_id": created.Session.ID,
		"summary":           "删除旧项目目录",
		"operations":        []map[string]any{{"operation": "delete", "path": "old-project"}},
	}
	result, err := rt.toolChangeExecute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	if response["status"] != "waiting_confirmation" {
		t.Fatalf("directory delete must wait for semantic confirmation: %+v", response)
	}
	pendingData, _ := response["data"].(map[string]any)
	confirm := mcp.CallToolRequest{}
	confirm.Params.Arguments = map[string]any{
		"intent": "用户已确认删除旧项目目录", "remote_session_id": created.Session.ID,
		"changeset_id": pendingData["changeset_id"], "expected_digest": pendingData["digest"],
		"confirmation_token": pendingData["confirmation_token"],
	}
	confirmedResult, err := rt.toolChangeExecute(context.Background(), confirm)
	if err != nil {
		t.Fatal(err)
	}
	response = decodeToolResult(t, confirmedResult)
	if response["status"] != "ok" {
		t.Fatalf("confirmed directory delete did not apply: %+v", response)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("directory was not removed: %v", err)
	}
	data, _ := response["data"].(map[string]any)
	files, _ := data["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("directory delete result missing file summary: %+v", data)
	}
	fileData, _ := files[0].(map[string]any)
	if fileData["is_directory"] != true || fileData["rollback"] != "retained_backup" {
		t.Fatalf("directory delete result missing safety metadata: %+v", fileData)
	}
	deleteSummary, _ := data["delete_summary"].(map[string]any)
	if deleteSummary["mode"] != "directory_recursive" || deleteSummary["top_level_directories"] != float64(1) ||
		deleteSummary["deleted_files"] != float64(1) || deleteSummary["deleted_directories"] != float64(2) {
		t.Fatalf("directory delete summary = %+v", deleteSummary)
	}
	if display, _ := deleteSummary["display"].(string); !strings.Contains(display, "目录级递归删除") || strings.Contains(display, "总大小") {
		t.Fatalf("directory delete display = %q", display)
	}
}

func TestChangeSummaryIncludesPerFileDiffPreview(t *testing.T) {
	item := changeset.Changeset{
		ID: "chg_preview",
		Files: []changeset.FileChange{{
			Operation: "update",
			Path:      "ChatGPT-互联网医院小程序修复.txt",
			Original:  []byte("旧消息链路\n"),
			Proposed:  []byte("新消息链路\n"),
		}},
	}

	dto := changeSummaryDTO(item)
	files, ok := dto["files"].([]map[string]any)
	if !ok || len(files) != 1 {
		t.Fatalf("per-file DTO missing: %+v", dto["files"])
	}
	if files[0]["path"] != item.Files[0].Path {
		t.Fatalf("per-file path = %v", files[0]["path"])
	}
	diff, _ := files[0]["diff"].(string)
	if !strings.Contains(diff, "-旧消息链路") || !strings.Contains(diff, "+新消息链路") {
		t.Fatalf("per-file diff preview must include concrete changes: %q", diff)
	}
}

func TestChangeSummaryIncludesExactExpectedDigestRecovery(t *testing.T) {
	item := changeset.Changeset{
		ID:              "chg_digest",
		RemoteSessionID: "session_digest",
		Status:          "draft",
		Digest:          "sha256:expected",
		Summary:         "digest recovery",
	}
	dto := changeSummaryDTO(item)
	if dto["digest"] != item.Digest || dto["expected_digest"] != item.Digest {
		t.Fatalf("digest fields must be identical: %+v", dto)
	}
	nextAction, _ := dto["next_action"].(map[string]any)
	if nextAction["tool"] != "change_apply" {
		t.Fatalf("missing change_apply recovery: %+v", dto)
	}
	arguments, _ := nextAction["arguments"].(map[string]any)
	if arguments["changeset_id"] != item.ID || arguments["expected_digest"] != item.Digest {
		t.Fatalf("recovery arguments must copy the exact digest: %+v", nextAction)
	}
}

func TestChangeApplyDigestConflictIncludesExactRecovery(t *testing.T) {
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
	prepare := mcp.CallToolRequest{}
	prepare.Params.Arguments = map[string]any{
		"intent":            "prepare a digest recovery test",
		"remote_session_id": created.Session.ID,
		"summary":           "digest recovery",
		"apply":             false,
		"operations": []map[string]any{
			{"operation": "create", "path": "digest-recovery.txt", "content": "content\n"},
		},
	}
	preparedResult, err := rt.toolChangeExecute(context.Background(), prepare)
	if err != nil {
		t.Fatal(err)
	}
	prepared := decodeToolResult(t, preparedResult)
	data, _ := prepared["data"].(map[string]any)
	changesetID, _ := data["changeset_id"].(string)
	digest, _ := data["digest"].(string)
	if changesetID == "" || digest == "" {
		t.Fatalf("prepared response missing digest contract: %+v", prepared)
	}
	apply := mcp.CallToolRequest{}
	apply.Params.Arguments = map[string]any{
		"intent":            "apply with an intentionally invalid digest",
		"remote_session_id": created.Session.ID,
		"changeset_id":      changesetID,
		"expected_digest":   "+211 −0",
	}
	conflictResult, err := rt.toolChangeExecute(context.Background(), apply)
	if err != nil {
		t.Fatal(err)
	}
	conflict := decodeToolResult(t, conflictResult)
	errorBody, _ := conflict["error"].(map[string]any)
	if errorBody["code"] != "PATCH_CONFLICT" {
		t.Fatalf("unexpected digest conflict: %+v", conflict)
	}
	details, _ := errorBody["details"].(map[string]any)
	if details["changeset_id"] != changesetID || details["expected_digest"] != digest {
		t.Fatalf("digest recovery details=%+v, want id=%q digest=%q", details, changesetID, digest)
	}
	recovery, _ := errorBody["recovery"].(map[string]any)
	arguments, _ := recovery["arguments"].(map[string]any)
	if recovery["tool"] != "change_apply" || arguments["changeset_id"] != changesetID || arguments["expected_digest"] != digest {
		t.Fatalf("digest recovery action=%+v", recovery)
	}
	if _, err := os.Stat(filepath.Join(registered.Path, "digest-recovery.txt")); !os.IsNotExist(err) {
		t.Fatalf("digest conflict must not write the file: %v", err)
	}
}

func TestChangeSummaryExplainsMixedDeleteScope(t *testing.T) {
	dto := changeSummaryDTO(changeset.Changeset{Files: []changeset.FileChange{
		{Operation: "delete", Path: "src", OriginalMode: uint32(os.ModeDir), DeletedFiles: 10, DeletedDirs: 3},
		{Operation: "delete", Path: "README.md", DeletedFiles: 1},
	}})
	summary, _ := dto["delete_summary"].(map[string]any)
	if summary["mode"] != "mixed" || summary["top_level_directories"] != 1 || summary["top_level_files"] != 1 ||
		summary["deleted_files"] != 11 || summary["deleted_directories"] != 3 {
		t.Fatalf("mixed delete summary = %+v", summary)
	}
	display, _ := summary["display"].(string)
	if !strings.Contains(display, "目录级递归删除") || !strings.Contains(display, "逐项删除 1 个顶层文件") || strings.Contains(display, "总大小") {
		t.Fatalf("mixed delete display = %q", display)
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
		"intent":            "apply an oversized patch",
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
	if response["status"] != "failed" {
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
		"intent":            "replace a file with a stale revision",
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
	t.Logf("change_prepare raw result: %+v", result)
	response := decodeToolResult(t, result)
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "REVISION_REQUIRED" {
		t.Fatalf("missing revision must map to REVISION_REQUIRED, got %v: %+v", errorBody["code"], response)
	}
	details, _ := errorBody["details"].(map[string]any)
	nextAction, _ := details["next_action"].(map[string]any)
	if nextAction["tool"] != "source_read" {
		t.Fatalf("REVISION_REQUIRED must carry a source_read recovery action: %+v", details)
	}
	arguments, _ := nextAction["arguments"].(map[string]any)
	if arguments["session_id"] != created.Session.ID {
		t.Fatalf("recovery action must preserve the session: %+v", arguments)
	}
}

func TestUpdateMissingRevisionSuggestsTargetFileRead(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	if err := os.WriteFile(filepath.Join(registered.Path, "update-me.txt"), []byte("update me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"intent":            "update without a revision to test recovery",
		"remote_session_id": created.Session.ID, "summary": "update without revision",
		"operations": []map[string]any{{"operation": "update", "path": "update-me.txt", "patch": "@@ -1 +1 @@\n-update me\n+updated\n"}},
	}
	result, err := rt.toolChangeExecute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "REVISION_REQUIRED" {
		t.Fatalf("missing update revision must map to REVISION_REQUIRED: %+v", response)
	}
	details, _ := errorBody["details"].(map[string]any)
	nextAction, _ := details["next_action"].(map[string]any)
	arguments, _ := nextAction["arguments"].(map[string]any)
	if nextAction["tool"] != "source_read" || arguments["path"] != "update-me.txt" {
		t.Fatalf("update recovery must target the missing revision file: %+v", details)
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
		"intent":            "prepare the duplicate-path change",
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
	if action["tool"] != "change_prepare" {
		t.Fatalf("duplicate path must suggest change_prepare: %+v", details)
	}
}

func TestChangePrepareChainsExactEditsForSamePath(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	original := []byte("one\ntwo\nthree\n")
	if err := os.WriteFile(filepath.Join(registered.Path, "multi.txt"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"intent":            "prepare chained exact edits",
		"remote_session_id": created.Session.ID,
		"summary":           "chained exact edits",
		"operations": []any{
			map[string]any{
				"operation": "replace_exact", "path": "multi.txt",
				"base_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256(original)),
				"match":       "one", "replacement": "ONE",
			},
			map[string]any{
				"operation": "replace_exact", "path": "multi.txt",
				"base_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256(original)),
				"match":       "three", "replacement": "THREE",
			},
		},
	}
	result, err := rt.toolHandlers["change_prepare"](context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	arcEnvelope := decodeARCEnvelope(t, result)
	resultData := arcEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)
	data, _ := resultData["data"].(map[string]any)
	if data["changeset_id"] == nil {
		t.Fatalf("chained exact edits must prepare successfully: %+v", data)
	}
}

func TestChangePreparePatchFormatErrorExplainsUnifiedDiff(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	if err := os.WriteFile(filepath.Join(registered.Path, "demo.txt"), []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"intent":            "prepare an apply_patch style update",
		"remote_session_id": created.Session.ID,
		"summary":           "patch format",
		"operations": []any{map[string]any{
			"operation":   "update",
			"path":        "demo.txt",
			"base_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("line one\nline two\n"))),
			"patch":       "*** Begin Patch\n*** Update File: demo.txt\n@@\n line one\n+line three\n*** End Patch",
		}},
	}
	result, err := rt.toolHandlers["change_prepare"](context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	arcEnvelope := decodeARCEnvelope(t, result)
	resultData := arcEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)
	data, _ := resultData["data"].(map[string]any)
	errorBody, _ := data["error"].(map[string]any)
	if errorBody["code"] != "PATCH_HUNKS_OVERLAP" {
		t.Fatalf("unexpected error code: %v", errorBody["code"])
	}
	message, _ := errorBody["message"].(string)
	for _, phrase := range []string{"unified diff", "*** Begin Patch", "replace_exact"} {
		if !strings.Contains(message, phrase) {
			t.Fatalf("patch format error must explain %q: %s", phrase, message)
		}
	}
	details, _ := errorBody["details"].(map[string]any)
	if details["patch_format_guidance"] == nil || details["patch_guidance"] == nil {
		t.Fatalf("patch format details missing guidance: %+v", details)
	}
}

func TestChangePrepareHunkCountErrorExplainsHeaderCounts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	if err := os.WriteFile(filepath.Join(registered.Path, "demo.txt"), []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"intent":            "prepare a hunk with wrong counts",
		"remote_session_id": created.Session.ID,
		"summary":           "hunk counts",
		"operations": []any{map[string]any{
			"operation":   "update",
			"path":        "demo.txt",
			"base_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("line one\nline two\n"))),
			"patch":       "@@ -1,2 +1,3 @@\n line one\n+line three\n",
		}},
	}
	result, err := rt.toolHandlers["change_prepare"](context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	arcEnvelope := decodeARCEnvelope(t, result)
	resultData := arcEnvelope["mcpx"].(map[string]any)["result"].(map[string]any)
	data, _ := resultData["data"].(map[string]any)
	errorBody, _ := data["error"].(map[string]any)
	if errorBody["code"] != "PATCH_HUNKS_OVERLAP" {
		t.Fatalf("unexpected error code: %v", errorBody["code"])
	}
	message, _ := errorBody["message"].(string)
	for _, phrase := range []string{"hunk 头", "必须与实际", "replace_exact"} {
		if !strings.Contains(message, phrase) {
			t.Fatalf("hunk count error must explain %q: %s", phrase, message)
		}
	}
	details, _ := errorBody["details"].(map[string]any)
	if details["patch_format_guidance"] == nil {
		t.Fatalf("hunk count details missing format guidance: %+v", details)
	}
}

func TestChangePrepareDeleteCreateConflictRequiresSeparateCalls(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	if err := os.WriteFile(filepath.Join(registered.Path, "same.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"intent":            "replace a file in separate audited steps",
		"remote_session_id": created.Session.ID,
		"action":            "prepare",
		"summary":           "replace same path",
		"operations": []map[string]any{
			{"operation": "delete", "path": "same.txt"},
			{"operation": "create", "path": "same.txt", "content": "new\n"},
		},
	}
	result, err := rt.toolChangeManage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "DELETE_CREATE_CONFLICT" {
		t.Fatalf("unexpected error code: %v", errorBody["code"])
	}
	if !strings.Contains(fmt.Sprint(errorBody["message"]), "separate change_execute calls") {
		t.Fatalf("error message did not explain split workflow: %+v", errorBody)
	}
	details, _ := errorBody["details"].(map[string]any)
	actions, _ := details["next_actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("split workflow recovery actions=%+v", details)
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
