package observation

import (
	"context"
	"path/filepath"
	"testing"

	"mcpx/internal/state"
)

func openObservationTestDB(t *testing.T) *state.Store {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state", "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestStoreListFiltersWorkspaceAndSequence(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	first, err := store.Append(context.Background(), Event{Workspace: "mcpx", Type: TypeToolStarted, Intent: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence <= 0 {
		t.Fatalf("sequence=%d", first.Sequence)
	}
	if _, err := store.Append(context.Background(), Event{Workspace: "other", Type: TypeToolStarted, Intent: "ignore"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.List(context.Background(), "mcpx", first.Sequence-1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Sequence != first.Sequence || got[0].Intent != "inspect" {
		t.Fatalf("events=%+v", got)
	}
}

func TestStoreRejectsInvalidEventPayload(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	if _, err := store.Append(context.Background(), Event{
		Workspace: "mcpx",
		Type:      TypeToolCompleted,
		Output:    []byte("not-json"),
	}); err == nil {
		t.Fatal("invalid JSON output was accepted")
	}
}

func TestStoreBoundsHistoryLimit(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	for i := 0; i < MaxHistory+5; i++ {
		if _, err := store.Append(context.Background(), Event{Workspace: "mcpx", Type: TypeObserverNotice}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.List(context.Background(), "mcpx", 0, MaxHistory+5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxHistory {
		t.Fatalf("got %d events, want %d", len(got), MaxHistory)
	}
}

func TestStoreHistoryReturnsRecentEventsInAscendingOrder(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	for i := 0; i < 3; i++ {
		if _, err := store.Append(context.Background(), Event{Workspace: "mcpx", Type: TypeObserverNotice, Summary: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.History(context.Background(), "mcpx", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Summary != "b" || events[1].Summary != "c" || events[0].Sequence >= events[1].Sequence {
		t.Fatalf("events=%+v", events)
	}
}
