package changeset

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestPrepareIdempotentWithOptionsReturnsCommittedChangeset(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{"value.txt": "old\n"})
	service := NewService(store.DB())
	ctx := context.Background()
	operation := Operation{
		Operation:      "update",
		Path:           "value.txt",
		ExpectedSHA256: hashBytes([]byte("old\n")),
		Content:        "new\n",
	}

	first, replayed, err := service.PrepareIdempotentWithOptions(ctx, remoteID, principal.ID, "request-1", workspace, "update value", []Operation{operation}, PrepareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first request was unexpectedly replayed")
	}

	second, replayed, err := service.PrepareIdempotentWithOptions(ctx, remoteID, principal.ID, "request-1", workspace, "different summary", []Operation{operation}, PrepareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed || second.ID != first.ID {
		t.Fatalf("retry did not return original changeset: first=%+v second=%+v replayed=%v", first, second, replayed)
	}

	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM changesets WHERE remote_session_id = ?`, remoteID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent prepare created %d changesets, want 1", count)
	}
}

func TestApplyRollsBackWhenLaterFileFails(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{
		"a.txt": "a-old\n",
		"b.txt": "b-old\n",
	})
	service := NewService(store.DB())
	service.beforeApply = func(item FileChange) error {
		if item.Ordinal == 1 {
			return errors.New("injected apply failure")
		}
		return nil
	}
	ctx := context.Background()
	prepared, err := service.Prepare(ctx, remoteID, principal.ID, workspace, "two files", []Operation{
		{Operation: "update", Path: "a.txt", ExpectedSHA256: hashBytes([]byte("a-old\n")), Content: "a-new\n"},
		{Operation: "update", Path: "b.txt", ExpectedSHA256: hashBytes([]byte("b-old\n")), Content: "b-new\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(ctx, prepared.ID, workspace); err == nil {
		t.Fatal("injected apply failure unexpectedly succeeded")
	}
	assertFile(t, filepath.Join(workspace, "a.txt"), "a-old\n")
	assertFile(t, filepath.Join(workspace, "b.txt"), "b-old\n")
	got, err := service.Get(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("failed apply status=%q, want failed", got.Status)
	}
	var journalStatus string
	if err := store.DB().QueryRow(`SELECT status FROM change_journals WHERE changeset_id = ?`, prepared.ID).Scan(&journalStatus); err != nil {
		t.Fatal(err)
	}
	if journalStatus != "rolled_back" {
		t.Fatalf("journal status=%q, want rolled_back", journalStatus)
	}
}

func TestApplyRemovesCreatedParentDirectoriesOnRollback(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	service := NewService(store.DB())
	service.beforeApply = func(item FileChange) error {
		if item.Ordinal == 1 {
			return errors.New("injected apply failure")
		}
		return nil
	}
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "rollback nested files", []Operation{
		{Operation: "create", Path: "nested/first.txt", Content: "first\n"},
		{Operation: "create", Path: "nested/second.txt", Content: "second\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); err == nil {
		t.Fatal("injected apply failure unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(workspace, "nested")); !os.IsNotExist(err) {
		t.Fatalf("created parent directory was not removed: %v", err)
	}
}

func TestApplyRollsBackDeleteAfterDirectorySyncFailure(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{"delete.txt": "restore me\n"})
	service := NewService(store.DB())
	ctx := context.Background()
	prepared, err := service.Prepare(ctx, remoteID, principal.ID, workspace, "delete file", []Operation{{
		Operation: "delete", Path: "delete.txt", ExpectedSHA256: hashBytes([]byte("restore me\n")),
	}})
	if err != nil {
		t.Fatal(err)
	}

	originalSync := syncDirectory
	callCount := 0
	syncDirectory = func(path string) error {
		callCount++
		if callCount == 1 {
			return errors.New("injected directory sync failure")
		}
		return originalSync(path)
	}
	t.Cleanup(func() { syncDirectory = originalSync })

	if _, err := service.Apply(ctx, prepared.ID, workspace); err == nil {
		t.Fatal("directory sync failure unexpectedly succeeded")
	}
	assertFile(t, filepath.Join(workspace, "delete.txt"), "restore me\n")
	var journalStatus string
	if err := store.DB().QueryRow(`SELECT status FROM change_journals WHERE changeset_id = ?`, prepared.ID).Scan(&journalStatus); err != nil {
		t.Fatal(err)
	}
	if journalStatus != "rolled_back" {
		t.Fatalf("journal status=%q, want rolled_back", journalStatus)
	}
}

func TestRecoverRollsBackInterruptedJournal(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{
		"a.txt": "a-old\n",
		"b.txt": "b-old\n",
	})
	service := NewService(store.DB())
	ctx := context.Background()
	prepared, err := service.Prepare(ctx, remoteID, principal.ID, workspace, "two files", []Operation{
		{Operation: "update", Path: "a.txt", ExpectedSHA256: hashBytes([]byte("a-old\n")), Content: "a-new\n"},
		{Operation: "update", Path: "b.txt", ExpectedSHA256: hashBytes([]byte("b-old\n")), Content: "b-new\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyOne(workspace, prepared.Files[0]); err != nil {
		t.Fatal(err)
	}
	insertApplyingJournal(t, store.DB(), "jrnl-recover", prepared, prepared.Files[:1])

	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(workspace, "a.txt"), "a-old\n")
	assertFile(t, filepath.Join(workspace, "b.txt"), "b-old\n")

	got, err := service.Get(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("recovered changeset status=%q, want failed", got.Status)
	}
	assertJournalStatus(t, store.DB(), "jrnl-recover", "rolled_back")
}

func TestRecoverRestoresInterruptedDirectoryDelete(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	directory := filepath.Join(workspace, "old-project")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "value.txt"), []byte("value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "delete directory", []Operation{{
		Operation: "delete", Path: "old-project",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyOneForChangeset(workspace, prepared.ID, prepared.Files[0]); err != nil {
		t.Fatal(err)
	}
	insertApplyingJournal(t, store.DB(), "jrnl-directory-recover", prepared, prepared.Files)

	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(directory, "nested", "value.txt"), "value\n")
	assertJournalStatus(t, store.DB(), "jrnl-directory-recover", "rolled_back")
}

func TestRecoverRefusesExternalModification(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{"value.txt": "old\n"})
	service := NewService(store.DB())
	ctx := context.Background()
	prepared, err := service.Prepare(ctx, remoteID, principal.ID, workspace, "update value", []Operation{{
		Operation: "update", Path: "value.txt", ExpectedSHA256: hashBytes([]byte("old\n")), Content: "new\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyOne(workspace, prepared.Files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	insertApplyingJournal(t, store.DB(), "jrnl-external", prepared, prepared.Files)

	if err := service.Recover(ctx); err == nil {
		t.Fatal("recovery unexpectedly overwrote an external modification")
	}
	assertFile(t, filepath.Join(workspace, "value.txt"), "external\n")
	assertJournalStatus(t, store.DB(), "jrnl-external", "failed")
}

func newChangesetFixture(t *testing.T, files map[string]string) (string, *state.Store, string, auth.Principal) {
	t.Helper()
	workspace := t.TempDir()
	for name, content := range files {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal := auth.Principal{ID: "owner", Kind: "test", SubjectHash: "owner-hash"}
	remote, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "test", WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, store, remote.Session.ID, principal
}

func insertApplyingJournal(t *testing.T, db *sql.DB, journalID string, prepared Changeset, applied []FileChange) {
	t.Helper()
	journal, err := marshalApplyJournal(prepared.ID, applied)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO change_journals(id, changeset_id, status, journal_json, created_at, updated_at)
		VALUES (?, ?, 'applying', ?, 1, 1)`, journalID, prepared.ID, string(journal)); err != nil {
		t.Fatal(err)
	}
}

func assertJournalStatus(t *testing.T, db *sql.DB, journalID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT status FROM change_journals WHERE id = ?`, journalID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("journal %s status=%q, want %q", journalID, got, want)
	}
}
