package idempotency

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcpx/internal/state"
)

func testStore(t *testing.T) (*Store, func()) {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db.DB())
	return store, func() { _ = db.Close() }
}

func testKey() Key {
	return Key{RemoteSessionID: "session-1", PrincipalID: "principal-1", Operation: "edit", Value: "key-1"}
}

func TestStoreReplayConflictAndPersistence(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()
	claim, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || claim.Kind != ClaimOwner {
		t.Fatalf("first claim=%+v err=%v", claim, err)
	}
	if err := store.UpdatePending(ctx, key, "sha256:first", []byte(`{"result":"prepared"}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"result":"done"}`), nil); err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(store.db).Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || second.Kind != ClaimReplay || string(second.Record.Response) != `{"result":"done"}` {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	conflict, err := store.Claim(ctx, key, "sha256:other", time.Hour)
	if err != nil || conflict.Kind != ClaimConflict {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestStoreMergesInFlightRequests(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()
	owner, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	waiting, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || waiting.Kind != ClaimWait {
		t.Fatalf("waiting=%+v err=%v", waiting, err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"ok":true}`), nil); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Wait(ctx, waiting, key)
	if err != nil || string(replayed.Response) != `{"ok":true}` {
		t.Fatalf("wait replay=%+v err=%v", replayed, err)
	}
}
