//go:build !windows

package observation

import (
	"context"
	"strings"
	"testing"
)

func TestSecondSocketServerDoesNotUnlinkActiveSocket(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	broker := NewBroker()
	defer broker.Close()
	path := testObserverPath(t)
	validate := func(workspace string) bool { return workspace == "mcpx" }

	first := NewSocketServer(path, store, broker, validate)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second := NewSocketServer(path, store, broker, validate)
	if err := second.Start(); err == nil || !strings.Contains(err.Error(), "already running") {
		if err == nil {
			_ = second.Close()
		}
		t.Fatalf("second server must reject active socket without unlinking it: %v", err)
	}

	conn, err := dialObserverSocket(context.Background(), path)
	if err != nil {
		t.Fatalf("first server socket became unreachable after second start attempt: %v", err)
	}
	_ = conn.Close()
}
