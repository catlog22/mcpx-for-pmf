package observation

import (
	"context"
	"testing"
	"time"
)

func TestQuerySupportsSemanticFiltersAndNewestCursor(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	created := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	first, err := store.Append(context.Background(), Event{
		Workspace: "mcpx", RemoteSessionID: "session_1", RequestID: "req_1", OperationID: "task_1",
		Tool: "skill_call", Type: TypeToolCompleted, Status: "succeeded", Purpose: "review refresh token",
		SkillName: "code-review", Summary: "review completed", CreatedAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), Event{
		Workspace: "mcpx", RemoteSessionID: "session_1", RequestID: "req_2", OperationID: "task_2",
		Tool: "command_run", Type: TypeCommandOutput, Status: "running", Command: "go test ./...",
		Summary: "test output", CreatedAt: created.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	events, cursor, err := store.Query(context.Background(), HistoryQuery{
		Workspace: "mcpx", SessionID: "session_1", Kinds: []string{"skill"}, Keyword: "refresh", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != first.Sequence || cursor == "" {
		t.Fatalf("events=%+v cursor=%q", events, cursor)
	}

	before, _, err := store.Query(context.Background(), HistoryQuery{Workspace: "mcpx", CreatedBefore: created.Add(30 * time.Second), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Sequence != first.Sequence {
		t.Fatalf("created_before filter returned %+v", before)
	}
}
