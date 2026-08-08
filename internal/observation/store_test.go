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
	first, err := store.Append(context.Background(), Event{Workspace: "mcpx", RequestID: "req_store", CallID: "call_store", Type: TypeToolStarted, Goal: "验证观测", Purpose: "inspect", Intent: "inspect", ReasoningSummary: "先验证持久化", ProgressSummary: "已完成准备", NextStep: "读取历史", PlanID: "pl_store", PlanTaskID: "pt_store", ExecutionTaskID: "task_store"})
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
	if len(got) != 1 || got[0].Sequence != first.Sequence || got[0].Intent != "inspect" || got[0].Goal != "验证观测" || got[0].ReasoningSummary != "先验证持久化" || got[0].ProgressSummary != "已完成准备" || got[0].NextStep != "读取历史" || got[0].PlanID != "pl_store" || got[0].PlanTaskID != "pt_store" || got[0].ExecutionTaskID != "task_store" || got[0].CallID != "call_store" || got[0].Phase != PhaseActionStarted {
		t.Fatalf("events=%+v", got)
	}
	filtered, _, err := store.Query(context.Background(), HistoryQuery{Workspace: "mcpx", CallID: "call_store", Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].Sequence != first.Sequence {
		t.Fatalf("correlation filter events=%+v err=%v", filtered, err)
	}
	planFiltered, _, err := store.Query(context.Background(), HistoryQuery{Workspace: "mcpx", PlanTaskIDs: []string{"pt_store"}, Limit: 10})
	if err != nil || len(planFiltered) != 1 || planFiltered[0].ExecutionTaskID != "task_store" {
		t.Fatalf("plan task filter events=%+v err=%v", planFiltered, err)
	}
	executionFiltered, _, err := store.Query(context.Background(), HistoryQuery{Workspace: "mcpx", ExecutionTaskIDs: []string{"task_store"}, Limit: 10})
	if err != nil || len(executionFiltered) != 1 || executionFiltered[0].PlanTaskID != "pt_store" {
		t.Fatalf("execution task filter events=%+v err=%v", executionFiltered, err)
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
