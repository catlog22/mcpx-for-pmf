package server

import (
	"context"
	"testing"
	"time"

	"mcpx/internal/envelope"
)

func TestObservationTargetPreservesValuesWithoutCancellation(t *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	parent, cancel := context.WithTimeout(context.WithValue(context.Background(), key, "authorization"), time.Hour)
	cancel()

	bridge := &observationBridge{
		resolve: func(ctx context.Context, request envelope.Request) (string, string) {
			if value := ctx.Value(key); value != "authorization" {
				t.Fatalf("context value=%v, want authorization", value)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("resolution inherited cancellation: %v", err)
			}
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("resolution inherited request deadline")
			}
			return "demo", request.RemoteSessionID
		},
	}

	workspace, remoteID := bridge.target(parent, envelope.Request{RemoteSessionID: "session-1"})
	if workspace != "demo" || remoteID != "session-1" {
		t.Fatalf("target=(%q, %q), want (demo, session-1)", workspace, remoteID)
	}
}
