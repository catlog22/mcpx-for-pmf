package filesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/file"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestSnapshotSurvivesDatabaseRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "mcpx.db")
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "source.go"), []byte("package source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateStore, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "snapshot-principal", Kind: "test", SubjectHash: "snapshot-subject"}
	created, err := remotesession.NewService(stateStore.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "project", WorkspacePath: workspace, Label: "snapshot test",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := file.TakeSnapshot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStore(stateStore.DB()).Save(context.Background(), created.Session.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := NewStore(reopened.DB()).Get(context.Background(), created.Session.ID, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WorkspaceRoot != snapshot.WorkspaceRoot || loaded.Hash["source.go"] != snapshot.Hash["source.go"] {
		t.Fatalf("loaded=%+v want=%+v", loaded, snapshot)
	}
}
