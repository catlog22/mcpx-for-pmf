package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestRegisterListReadAndDetectExternalChange(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("test report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := auth.Principal{ID: "artifact-principal", Kind: "test", SubjectHash: "artifact-subject"}
	created, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "project", WorkspacePath: workspace, Label: "artifact test",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	registered, err := service.Register(context.Background(), created.Session.ID, principal.ID, workspace, "report.txt", "Test report", "test_report", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if registered.ResourceURI != ResourceURI(created.Session.ID, registered.ID) {
		t.Fatalf("resource URI=%q", registered.ResourceURI)
	}
	listed, err := service.List(context.Background(), created.Session.ID, "test_report", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	read, err := service.Read(context.Background(), created.Session.ID, registered.ID, workspace, 0, 4)
	if err != nil || read.Data != "test" || read.Encoding != "utf-8" {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ReadAll(context.Background(), created.Session.ID, registered.ID, workspace, 8<<20); !errors.Is(err, ErrChanged) {
		t.Fatalf("ReadAll err=%v, want ErrChanged", err)
	}
}
