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

func TestStoreAbandonRemovesOnlyPendingRecords(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	owner, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	if err := store.Abandon(ctx, key, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Lookup(ctx, key); err != nil || ok {
		t.Fatalf("abandoned pending record must be gone: ok=%v err=%v", ok, err)
	}

	// A different fingerprint must not delete the owned record.
	owner, err = store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("re-owner=%+v err=%v", owner, err)
	}
	if err := store.Abandon(ctx, key, "sha256:other"); err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Lookup(ctx, key)
	if err != nil || !ok || record.State != StatePending {
		t.Fatalf("wrong-fingerprint Abandon must keep pending record: %+v ok=%v err=%v", record, ok, err)
	}

	// The in-process flight must survive a wrong-fingerprint Abandon: the
	// owner is still running, so an identical Claim waits for its completion
	// instead of reporting a foreign pending record.
	waiting, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || waiting.Kind != ClaimWait {
		t.Fatalf("wrong-fingerprint Abandon must keep the in-process flight: %+v err=%v", waiting, err)
	}
	if waiting.Done == nil {
		t.Fatal("ClaimWait must expose the owner's completion channel")
	}
	// The owner completes below; the wait channel must be released.
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"done":true}`), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiting.Done:
	default:
		t.Fatal("owner completion must release the waiting claim")
	}

	// A terminal record is protected: Abandon must not delete it.
	if err := store.Abandon(ctx, key, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || replay.Kind != ClaimReplay {
		t.Fatalf("terminal record must survive Abandon: %+v err=%v", replay, err)
	}
}
