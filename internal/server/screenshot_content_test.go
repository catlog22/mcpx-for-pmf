package server

import (
	"context"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/mcpresult"
	"mcpx/internal/remotesession"
	"mcpx/internal/screenshot"
)

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

func TestScreenshotResultIncludesImageContent(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, _ := rt.reg.Load().Get("demo")
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
