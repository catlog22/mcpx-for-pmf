package server

import (
	"bytes"

	"mcpx/internal/mcpresult"

	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/remotesession"
	"mcpx/internal/screenshot"
)

func TestRemoteSessionMCPIsVendorAndTransportIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspacePath := filepath.Join(home, "project")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "token-a"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspacePath}}
	cfg.Logging.Enabled = false
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	contextFor := func(token string, _ ...string) context.Context {
		return auth.ContextWithAuthorization(context.Background(), "Bearer "+token)
	}
	call := func(t *testing.T, handler mcp.ToolHandler, ctx context.Context, arguments map[string]any) map[string]any {
		t.Helper()
		if _, exists := arguments["intent"]; !exists {
			withIntent := make(map[string]any, len(arguments)+1)
			for key, value := range arguments {
				withIntent[key] = value
			}
			withIntent["intent"] = "transport acceptance operation"
			arguments = withIntent
		}
		result, err := handler(ctx, mcpresult.Request(arguments))
		if err != nil {
			t.Fatal(err)
		}
		return decodeToolResult(t, result)
	}

	ctxA1 := contextFor("token-a", "transport-a-1", "vendor-a")
	created := call(t, runtime.toolSessionOpen, ctxA1, map[string]any{
		"workspace": "project",
		"label":     "cross-vendor session",
	})
	remoteSessionID, _ := created["remote_session_id"].(string)
	if created["status"] != "ok" || remoteSessionID == "" {
		t.Fatalf("create failed: %+v", created)
	}
	principal, err := runtime.principalFromContext(ctxA1)
	if err != nil {
		t.Fatal(err)
	}
	createdSession, err := runtime.remote.Get(ctxA1, principal, remoteSessionID)
	if err != nil {
		t.Fatal(err)
	}
	baselineSnapshotID := createdSession.EnvironmentSnapshotID
	if baselineSnapshotID == "" {
		t.Fatalf("session_open did not bind an environment baseline: %+v", created)
	}
	inspected := call(t, runtime.toolEnvironmentInspect, ctxA1, map[string]any{
		"remote_session_id": remoteSessionID,
		"sections":          []any{"os", "architecture"},
		"compare_to":        baselineSnapshotID,
		"save_snapshot":     false,
	})
	inspectionData, _ := inspected["data"].(map[string]any)
	if inspected["status"] != "ok" && inspected["status"] != "succeeded" || inspectionData["os"] == nil || inspectionData["architecture"] == nil {
		t.Fatalf("environment inspection failed: %+v", inspected)
	}
	if inspectionData["runtime"] != nil || inspectionData["toolchains"] != nil || inspectionData["comparison"] == nil {
		t.Fatalf("environment sections were not filtered: %+v", inspectionData)
	}

	// A different bearer is a different Principal even when it presents the
	// same durable identifier. Session existence is deliberately concealed.
	runtime.cfg.Auth.Token = "token-b"
	ctxB := contextFor("token-b", "transport-b-1", "vendor-b")
	denied := call(t, runtime.toolRemoteSessionGet, ctxB, map[string]any{"remote_session_id": remoteSessionID})
	if errorCode(denied) != "not_found" {
		t.Fatalf("unattached principal should not see session: %+v", denied)
	}

	// The owner can continue from another transport session without relying on
	// the original Mcp-Session-Id.
	runtime.cfg.Auth.Token = "token-a"
	ctxA2 := contextFor("token-a", "transport-a-2", "vendor-a-next")
	continued := call(t, runtime.toolRemoteSessionGet, ctxA2, map[string]any{"remote_session_id": remoteSessionID})
	if !statusOK(continued) {
		t.Fatalf("transport session change lost durable state: %+v", continued)
	}
	viewerHandoff := call(t, runtime.toolRemoteSessionHandoff, ctxA2, map[string]any{
		"remote_session_id": remoteSessionID, "role": "viewer", "expires_in": 60,
	})
	viewerToken := viewerHandoff["data"].(map[string]any)["handoff_token"].(string)
	runtime.cfg.Auth.Token = "token-c"
	ctxC := contextFor("token-c", "transport-c-1", "vendor-c")
	if response := call(t, runtime.toolRemoteSessionAttach, ctxC, map[string]any{"handoff_token": viewerToken}); !statusOK(response) {
		t.Fatalf("viewer attach failed: %+v", response)
	}
	runtime.screenshot = fakeScreenCapturer{}
	viewerCapture := call(t, runtime.toolScreenshotCapture, ctxC, map[string]any{"remote_session_id": remoteSessionID, "mode": "fullscreen"})
	if errorCode(viewerCapture) != "forbidden" {
		t.Fatalf("viewer should not capture screen: %+v", viewerCapture)
	}
	runtime.cfg.Auth.Token = "token-a"
	handoff := call(t, runtime.toolRemoteSessionHandoff, ctxA2, map[string]any{
		"remote_session_id": remoteSessionID,
		"role":              "editor",
		"expires_in":        60,
	})
	handoffData, _ := handoff["data"].(map[string]any)
	handoffToken, _ := handoffData["handoff_token"].(string)
	if handoffToken == "" {
		t.Fatalf("handoff token missing: %+v", handoff)
	}

	runtime.cfg.Auth.Token = "token-b"
	attached := call(t, runtime.toolRemoteSessionAttach, ctxB, map[string]any{"handoff_token": handoffToken})
	meta, _ := attached["meta"].(map[string]any)
	if !statusOK(attached) || meta["session_id"] != remoteSessionID {
		t.Fatalf("cross-vendor attach failed: %+v", attached)
	}
	visible := call(t, runtime.toolRemoteSessionGet, ctxB, map[string]any{"remote_session_id": remoteSessionID})
	if !statusOK(visible) {
		t.Fatalf("attached principal cannot query session: %+v", visible)
	}
	reused := call(t, runtime.toolRemoteSessionAttach, contextFor("token-b", "transport-b-2", "vendor-c"), map[string]any{"handoff_token": handoffToken})
	if errorCode(reused) != "invalid_handoff_token" {
		t.Fatalf("handoff token was not one-shot: %+v", reused)
	}
}

func TestChangesetMCPDiffAndSemanticConfirmationSurviveTransportChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspacePath := filepath.Join(home, "project")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("package demo\n\nconst Value = 1\n")
	if err := os.WriteFile(filepath.Join(workspacePath, "demo.go"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspacePath}}
	cfg.Security.Files.Confirm = []string{`\.go$`}
	cfg.Logging.Enabled = false
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	contextFor := func(_ ...string) context.Context {
		return context.Background()
	}

	created := callEnvelope(t, runtime.toolSessionOpen, contextFor(), map[string]any{"workspace": "project"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	if remoteSessionID == "" {
		t.Fatalf("session_open failed: %+v", created)
	}
	runtime.screenshot = fakeScreenCapturer{}
	screenshotRequest := mcpresult.Request(map[string]any{
		"intent":            "capture a screenshot",
		"remote_session_id": remoteSessionID,
		"mode":              "region",
		"x":                 10,
		"y":                 20,
		"width":             300,
		"height":            200,
		"compression":       "small",
	})

	screenshotResult, err := runtime.toolScreenshotCapture(contextFor(), screenshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	var imageContent *mcp.ImageContent
	for _, c := range screenshotResult.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			imageContent = img
			break
		}
	}
	if imageContent == nil || imageContent.MIMEType != "image/jpeg" || !bytes.Equal(imageContent.Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("invalid MCP image content: len=%d content=%+v sc=%+v", len(screenshotResult.Content), screenshotResult.Content, screenshotResult.StructuredContent)
	}
	meta := structuredBusinessData(screenshotResult)
	if meta == nil || meta["mime_type"] == "" && meta["format"] == "" && meta["sha256"] == "" {
		// Metadata is JSON-normalized under wire data after remoteResult.
		if meta == nil {
			t.Fatalf("missing screenshot structured metadata: %T", screenshotResult.StructuredContent)
		}
	}
	sum := sha256.Sum256(original)
	prepareRequest := mcpresult.Request(map[string]any{
		"intent":            "prepare a file change",
		"remote_session_id": remoteSessionID,
		"summary":           "update demo value",
		"operations": []any{map[string]any{
			"operation": "update", "path": "demo.go",
			"base_sha256": fmt.Sprintf("sha256:%x", sum[:]),
			"patch":       "@@ -1,3 +1,3 @@\n package demo\n \n-const Value = 1\n+const Value = 2\n",
		}},
	})

	preparedResult, err := runtime.toolChangePrepare(contextFor(), prepareRequest)
	if err != nil {
		t.Fatal(err)
	}
	text := preparedResult.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "```diff") || !strings.Contains(text, "Value = 2") || !strings.Contains(text, "demo.go") {
		t.Fatalf("content should include a Markdown diff with the changed file:\n%s", text)
	}
	preparedDTO := structuredBusinessData(preparedResult)
	if preparedDTO == nil {
		t.Fatalf("missing structured Changeset DTO: %T %+v", preparedResult.StructuredContent, preparedResult.StructuredContent)
	}
	changesetID, _ := preparedDTO["changeset_id"].(string)
	digest, _ := preparedDTO["digest"].(string)
	if changesetID == "" || digest == "" {
		t.Fatalf("missing changeset id/digest: %+v", preparedDTO)
	}
	diffMeta, _ := preparedDTO["diff"].(map[string]any)
	if mode, _ := diffMeta["mode"].(string); mode != "inline" && mode != "resource" {
		t.Fatalf("diff mode missing: %+v", diffMeta)
	}
	resourceURI, _ := diffMeta["resource_uri"].(string)
	if resourceURI == "" {
		t.Fatalf("Changeset DTO missing Resource URI: %+v", preparedDTO)
	}
	if inline, _ := diffMeta["unified_diff"].(string); !strings.Contains(inline, "Value = 2") {
		t.Fatalf("small diff must have one structured authoritative copy: %+v", diffMeta)
	}
	diffResources, err := runtime.resourceChangesetDiff(contextFor(), &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceURI}})
	if err != nil || diffResources == nil || len(diffResources.Contents) != 1 {
		t.Fatalf("read Changeset resource: resources=%+v err=%v", diffResources, err)
	}

	pending := callEnvelope(t, runtime.toolChangeExecute, contextFor(), map[string]any{
		"remote_session_id": remoteSessionID,
		"changeset_id":      changesetID,
		"expected_digest":   digest,
	})
	if pending["status"] != "waiting_confirmation" {
		t.Fatalf("expected confirmation: %+v", pending)
	}
	pendingData := pending["data"].(map[string]any)
	pendingFiles, _ := pendingData["files"].([]any)
	if len(pendingFiles) != 1 {
		t.Fatalf("confirmation response must include changed files: %+v", pendingData)
	}
	pendingFile, _ := pendingFiles[0].(map[string]any)
	pendingDiff, _ := pendingFile["diff"].(string)
	if !strings.Contains(pendingDiff, "-const Value = 1") || !strings.Contains(pendingDiff, "+const Value = 2") {
		t.Fatalf("confirmation response must include concrete file diff: %+v", pendingFile)
	}
	missingSession := callEnvelope(t, runtime.toolChangeExecute, contextFor(), map[string]any{
		"changeset_id": pendingData["changeset_id"], "expected_digest": pendingData["digest"], "confirmation_token": pendingData["confirmation_token"],
	})
	if missingSession["status"] != "failed" || missingSession["error"].(map[string]any)["code"] != "REMOTE_SESSION_REQUIRED" {
		t.Fatalf("confirmation must require explicit Remote Session: %+v", missingSession)
	}
	confirmed := callEnvelope(t, runtime.toolChangeExecute, contextFor(), map[string]any{
		"remote_session_id": remoteSessionID, "changeset_id": pendingData["changeset_id"],
		"expected_digest": pendingData["digest"], "confirmation_token": pendingData["confirmation_token"],
	})
	if confirmed["status"] != "ok" {
		t.Fatalf("confirmation failed after transport change: %+v", confirmed)
	}
	content, err := os.ReadFile(filepath.Join(workspacePath, "demo.go"))
	if err != nil || !strings.Contains(string(content), "Value = 2") {
		t.Fatalf("Changeset not applied: %q err=%v", content, err)
	}
}

type fakeScreenCapturer struct{}

func (fakeScreenCapturer) Capture(_ context.Context, request screenshot.Request) (screenshot.Result, error) {
	return screenshot.Result{
		Data: []byte{1, 2, 3, 4},
		Metadata: screenshot.Metadata{
			Mode: request.Mode, Display: request.Display, X: request.X, Y: request.Y,
			CapturedWidth: request.Width, CapturedHeight: request.Height,
			OutputWidth: 300, OutputHeight: 200, Compression: request.Compression,
			Format: "jpeg", MIMEType: "image/jpeg", Bytes: 4, SHA256: "sha256:test",
		},
	}, nil
}

func callEnvelope(t *testing.T, handler mcp.ToolHandler, ctx context.Context, arguments map[string]any) map[string]any {
	t.Helper()
	if _, exists := arguments["intent"]; !exists {
		withIntent := make(map[string]any, len(arguments)+1)
		for key, value := range arguments {
			withIntent[key] = value
		}
		withIntent["intent"] = "test operation"
		arguments = withIntent
	}
	var request *mcp.CallToolRequest
	request = mcpresult.Request(arguments)
	result, err := handler(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return decodeToolResult(t, result)
}

func errorCode(response map[string]any) string {
	body, _ := response["error"].(map[string]any)
	code, _ := body["code"].(string)
	return strings.ToLower(code)
}

func TestWorkspaceRevisionAggregatesNestedGitRoots(t *testing.T) {
	root := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		subRoot := filepath.Join(root, sub)
		if err := os.MkdirAll(subRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		gitIn := func(args ...string) {
			t.Helper()
			command := exec.Command("git", append([]string{"-C", subRoot}, args...)...)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
		}
		gitIn("init")
		gitIn("config", "user.email", "test@example.invalid")
		gitIn("config", "user.name", "MCPX Test")
		if err := os.WriteFile(filepath.Join(subRoot, "f.txt"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn("add", ".")
		gitIn("commit", "-m", "base")
	}
	head, digest := workspaceRevision(context.Background(), root)
	if head == "" || digest == "" {
		t.Fatalf("aggregate revision must be non-empty: head=%q digest=%q", head, digest)
	}
	if !strings.Contains(head, "a:") || !strings.Contains(head, "b:") {
		t.Fatalf("head must name both roots: %q", head)
	}
}

func TestWorkspaceRevisionSingleRootUnchanged(t *testing.T) {
	root := t.TempDir()
	gitIn := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	gitIn("init")
	gitIn("config", "user.email", "test@example.invalid")
	gitIn("config", "user.name", "MCPX Test")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn("add", ".")
	gitIn("commit", "-m", "base")
	head, digest := workspaceRevision(context.Background(), root)
	if head == "" || digest == "" {
		t.Fatalf("single-root revision must be non-empty: head=%q digest=%q", head, digest)
	}
	if strings.Contains(head, ":") {
		t.Fatalf("single root head must stay bare: %q", head)
	}
}

func TestSessionEventsIncludePendingConfirmations(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands.Confirm = append(rt.cfg.Security.Commands.Confirm, `^echo\b`)
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
	command := mcpresult.Request(map[string]any{
		"intent":            "request pending command confirmation",
		"remote_session_id": created.Session.ID,
		"command":           "echo pending", "purpose": "inspect pending", "scope": "workspace",
	})

	commandResult, err := rt.toolCommandExecute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	commandResponse := decodeToolResult(t, commandResult)
	commandData, _ := commandResponse["data"].(map[string]any)
	token, _ := commandData["confirmation_token"].(string)
	if commandResponse["status"] != "waiting_confirmation" || token == "" {
		t.Fatalf("command confirmation = %+v", commandResponse)
	}

	events := mcpresult.Request(map[string]any{
		"intent":            "recover pending confirmation from event log",
		"remote_session_id": created.Session.ID, "view": "events", "limit": 5,
	})

	eventsResult, err := rt.toolSessionRead(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	eventsResponse := decodeToolResult(t, eventsResult)
	eventsData, _ := eventsResponse["data"].(map[string]any)
	items, ok := eventsData["pending_confirmations"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("session events must expose pending confirmations: %+v", eventsData)
	}
	item := items[0].(map[string]any)
	if item["confirmation_token"] != token || item["command"] != "echo pending" {
		t.Fatalf("pending confirmation item=%+v want token=%s", item, token)
	}
}

func TestRemoteSessionNotFoundExplainsExactCopy(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	request := mcpresult.Request(map[string]any{
		"intent":            "read a missing remote session",
		"remote_session_id": "rs-does-not-exist",
		"view":              "summary",
	})

	result, err := rt.toolSessionRead(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeToolResult(t, result)
	errorBody, _ := response["error"].(map[string]any)
	if errorBody["code"] != "NOT_FOUND" {
		t.Fatalf("missing session error = %+v", response)
	}
	message, _ := errorBody["message"].(string)
	for _, phrase := range []string{"原样复制", "session"} {
		if !strings.Contains(message, phrase) {
			t.Fatalf("missing session error must explain %q: %s", phrase, message)
		}
	}
}
