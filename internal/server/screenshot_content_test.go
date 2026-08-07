package server

import (
	"context"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/mcpresult"
	"mcpx/internal/remotesession"
	"mcpx/internal/screenshot"
)

func TestScreenshotResultIncludesImageContent(t *testing.T) {
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
	rt.screenshot = fakeScreenCapturer{}
	ctx := auth.ContextWithAuthorization(context.Background(), "Bearer developer-token")
	// open mode for newWorkspaceRuntime uses open auth - check
	res, err := rt.toolScreenshotCapture(ctx, mcpresult.Request(map[string]any{
		"intent": "capture", "remote_session_id": created.Session.ID, "mode": "fullscreen",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) < 2 {
		t.Fatalf("content len=%d sc=%+v", len(res.Content), res.StructuredContent)
	}
	_ = screenshot.Request{}
}
