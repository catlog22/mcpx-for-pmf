package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleTask(taskID, sessionID string, created time.Time) DelegatedTask {
	delivered := created.Add(2 * time.Second)
	return DelegatedTask{
		TaskID:          taskID,
		RemoteSessionID: sessionID,
		Workspace:       "demo",
		TargetOwnerID:   "11111111111111111111111111111111",
		SpawnPID:        4242,
		Action:          "steer",
		Message:         "run the migration",
		Purpose:         "verify the delegation loop",
		Status:          StatusDelivered,
		CreatedAt:       created,
		DeliveredAt:     &delivered,
	}
}

func TestRegistryPutAndGetRoundTrip(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	created := time.Now().UTC().Truncate(time.Millisecond)
	task := sampleTask("aabb", "sess-one", created)
	if err := reg.Put(task); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get("sess-one", "aabb")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != task.TaskID || got.RemoteSessionID != task.RemoteSessionID ||
		got.Workspace != task.Workspace || got.TargetOwnerID != task.TargetOwnerID ||
		got.SpawnPID != task.SpawnPID || got.Action != task.Action ||
		got.Message != task.Message || got.Purpose != task.Purpose ||
		got.Status != StatusDelivered || got.Error != "" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at mismatch: %v != %v", got.CreatedAt, created)
	}
	if got.DeliveredAt == nil || !got.DeliveredAt.Equal(created.Add(2*time.Second)) {
		t.Fatalf("delivered_at mismatch: %+v", got.DeliveredAt)
	}
	if got.CompletedAt != nil {
		t.Fatalf("completed_at should be nil: %+v", got.CompletedAt)
	}
	// One JSON file per task, under {home}/tasks/delegated/{session}/.
	if _, err := os.Stat(filepath.Join(reg.Root(), "sess-one", "aabb.json")); err != nil {
		t.Fatalf("registry file missing: %v", err)
	}
}

func TestRegistryPutOverwrites(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	task := sampleTask("aabb", "sess-one", time.Now().UTC())
	if err := reg.Put(task); err != nil {
		t.Fatal(err)
	}
	task.Status = StatusCompleted
	task.Result = "all green"
	task.ResultSummary = []string{"tests pass"}
	if err := reg.Put(task); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get("sess-one", "aabb")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted || got.Result != "all green" || len(got.ResultSummary) != 1 {
		t.Fatalf("overwrite lost fields: %+v", got)
	}
}

func TestRegistryGetMissing(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Get("sess-one", "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegistryListBySession(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	base := time.Now().UTC().Truncate(time.Millisecond)
	first := sampleTask("task-1", "sess-one", base)
	second := sampleTask("task-2", "sess-one", base.Add(time.Second))
	other := sampleTask("task-3", "sess-two", base)
	for _, task := range []DelegatedTask{second, first, other} {
		if err := reg.Put(task); err != nil {
			t.Fatal(err)
		}
	}
	// A result companion file must not surface as a registry entry.
	if err := os.WriteFile(reg.ResultPath("sess-one", "task-1"), []byte(`{"task_id":"task-1","status":"completed"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := reg.ListBySession("sess-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 tasks, got %d: %+v", len(list), list)
	}
	if list[0].TaskID != "task-1" || list[1].TaskID != "task-2" {
		t.Fatalf("expected oldest-first order, got %s, %s", list[0].TaskID, list[1].TaskID)
	}

	// Unknown session lists empty without error.
	empty, err := reg.ListBySession("sess-missing")
	if err != nil || len(empty) != 0 {
		t.Fatalf("expected empty list for unknown session, got %v err=%v", empty, err)
	}
}

func TestRegistryReadResult(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	if _, err := reg.ReadResult("sess-one", "aabb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing result, got %v", err)
	}
	completed := time.Now().UTC().Truncate(time.Millisecond)
	result := TaskResult{
		TaskID:        "aabb",
		Status:        StatusCompleted,
		Result:        "migration applied",
		ResultSummary: []string{"schema updated", "tests pass"},
		CompletedAt:   &completed,
	}
	raw := `{"task_id":"aabb","status":"completed","result":"migration applied","result_summary":["schema updated","tests pass"],"completed_at":"` + completed.Format(time.RFC3339Nano) + `"}`
	if err := os.MkdirAll(reg.SessionDir("sess-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg.ResultPath("sess-one", "aabb"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := reg.ReadResult("sess-one", "aabb")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != result.TaskID || got.Status != result.Status || got.Result != result.Result ||
		len(got.ResultSummary) != 2 || got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Fatalf("result mismatch: %+v", got)
	}
}

func TestRegistryRejectsInvalidIDs(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	valid := sampleTask("aabb", "sess-one", time.Now().UTC())
	for _, mutation := range []struct {
		name string
		task DelegatedTask
	}{
		{"empty task id", func() DelegatedTask { t := valid; t.TaskID = ""; return t }()},
		{"dot task id", func() DelegatedTask { t := valid; t.TaskID = ".."; return t }()},
		{"path escape task id", func() DelegatedTask { t := valid; t.TaskID = "../evil"; return t }()},
		{"empty session id", func() DelegatedTask { t := valid; t.RemoteSessionID = ""; return t }()},
		{"path escape session id", func() DelegatedTask { t := valid; t.RemoteSessionID = "a/b"; return t }()},
		{"missing status", func() DelegatedTask { t := valid; t.Status = ""; return t }()},
	} {
		if err := reg.Put(mutation.task); err == nil {
			t.Fatalf("%s: expected error", mutation.name)
		}
	}
	for _, session := range []string{"", "..", "a/b"} {
		if _, err := reg.Get(session, "aabb"); err == nil {
			t.Fatalf("Get(%q): expected error", session)
		}
		if _, err := reg.ListBySession(session); err == nil {
			t.Fatalf("ListBySession(%q): expected error", session)
		}
	}
}
