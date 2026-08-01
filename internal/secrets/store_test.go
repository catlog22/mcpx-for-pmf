package secrets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestOneShotAndCache(t *testing.T) {
	env := OneShotEnv("secret", "env:FOO")
	if len(env) != 1 || env[0] != "FOO=secret" {
		t.Fatalf("%v", env)
	}
	s := NewStore()
	s.Set("s1", "password", "p", true, 0)
	v, ok := s.Get("s1", "password")
	if !ok || v != "p" {
		t.Fatal(v, ok)
	}
}

func TestPendingMetadataPersistsButSecretValueDoesNot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "mcpx.db")
	stateStore, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "secret-principal", Kind: "test", SubjectHash: "secret-subject"}
	created, err := remotesession.NewService(stateStore.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "project", WorkspacePath: t.TempDir(), Label: "secret test",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewPersistentStore(stateStore.DB())
	store.Cache(created.Session.ID, map[string]string{"password": "never-persist-this"})
	requestID := store.PutPending(PendingSecret{RemoteSessionID: created.Session.ID, PrincipalID: principal.ID, Tool: "terminal_exec", Prompt: "password"})
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := NewPersistentStore(reopened.DB())
	if _, ok := restored.Get(created.Session.ID, "password"); ok {
		t.Fatal("secret value must not survive restart")
	}
	pending, ok := restored.TakePending(requestID)
	if !ok || pending.RemoteSessionID != created.Session.ID {
		t.Fatalf("pending ok=%v value=%+v", ok, pending)
	}
	var persisted string
	if err := reopened.DB().QueryRow(`SELECT payload_json FROM secret_requests WHERE id = ?`, requestID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "never-persist-this") {
		t.Fatal("secret value leaked into SQLite metadata")
	}
}
