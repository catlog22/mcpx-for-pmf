package changeset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestPrepareApplyHistoryAndRevert(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := auth.Principal{ID: "owner", Kind: "test", SubjectHash: "owner-hash"}
	remote, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "test", WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	prepared, err := service.Prepare(context.Background(), remote.Session.ID, principal.ID, workspace, "edit files", []Operation{
		{Operation: "update", Path: "a.txt", ExpectedSHA256: hashBytes([]byte("old\n")), Content: "new\n"},
		{Operation: "create", Path: "b.txt", Content: "created\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "draft" || !strings.Contains(prepared.UnifiedDiff, "--- a/a.txt") || !strings.Contains(prepared.UnifiedDiff, "+++ /dev/null") && !strings.Contains(prepared.UnifiedDiff, "+++ b/b.txt") {
		t.Fatalf("prepared: %+v\n%s", prepared, prepared.UnifiedDiff)
	}

	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("external\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("expected stale revision, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("preflight failure must not leave a partial new file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	applied, err := service.Apply(context.Background(), prepared.ID, workspace)
	if err != nil || applied.Status != "applied" {
		t.Fatalf("apply: %+v err=%v", applied, err)
	}
	assertFile(t, filepath.Join(workspace, "a.txt"), "new\n")
	assertFile(t, filepath.Join(workspace, "b.txt"), "created\n")
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated apply should conflict, got %v", err)
	}
	history, err := service.History(context.Background(), remote.Session.ID, 10)
	if err != nil || len(history) != 1 || history[0].Status != "applied" {
		t.Fatalf("history: %+v err=%v", history, err)
	}

	revert, err := service.PrepareRevert(context.Background(), prepared.ID, principal.ID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(revert.UnifiedDiff, "Revert") && revert.Summary != "Revert "+prepared.ID {
		t.Fatalf("revert metadata: %+v", revert)
	}
	if _, err := service.Apply(context.Background(), revert.ID, workspace); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(workspace, "a.txt"), "old\n")
	if _, err := os.Stat(filepath.Join(workspace, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("created file was not reverted: %v", err)
	}
	source, err := service.Get(context.Background(), prepared.ID)
	if err != nil || source.Status != "reverted" {
		t.Fatalf("source status after revert: %+v err=%v", source, err)
	}
}

func TestPrepareAppliesUnifiedPatchAtServer(t *testing.T) {
	workspace := t.TempDir()
	original := "<template>\n  <main>Old</main>\n</template>\n"
	if err := os.WriteFile(filepath.Join(workspace, "App.vue"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := auth.Principal{ID: "owner", Kind: "test", SubjectHash: "owner-hash"}
	remote, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "test", WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	patch := "@@ -1,3 +1,3 @@\n <template>\n-  <main>Old</main>\n+  <main>New</main>\n </template>\n"
	prepared, err := service.Prepare(context.Background(), remote.Session.ID, principal.ID, workspace, "update view", []Operation{{
		Operation: "update", Path: "App.vue", BaseSHA256: hashBytes([]byte(original)), Patch: patch,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.UnifiedDiff, "+  <main>New</main>") {
		t.Fatalf("prepared diff does not contain patch result:\n%s", prepared.UnifiedDiff)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(workspace, "App.vue"), "<template>\n  <main>New</main>\n</template>\n")
}

func TestPrepareRejectsPatchWithStaleContext(t *testing.T) {
	workspace := t.TempDir()
	original := "one\ntwo\n"
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := prepareOperation(workspace, 0, Operation{
		Operation: "update", Path: "value.txt", BaseSHA256: hashBytes([]byte(original)),
		Patch: "@@ -1,2 +1,2 @@\n one\n-missing\n+three\n",
	})
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("expected stale patch context, got %v", err)
	}
}

func TestPreparePatchPreservesUTF8BOMAndCRLF(t *testing.T) {
	workspace := t.TempDir()
	original := "\ufeffpackage demo\r\n\r\nconst Value = 1\r\n"
	if err := os.WriteFile(filepath.Join(workspace, "demo.go"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareOperation(workspace, 0, Operation{
		Operation: "update", Path: "demo.go", BaseSHA256: hashBytes([]byte(original)),
		Patch: "@@ -1,3 +1,3 @@\n \ufeffpackage demo\n \n-const Value = 1\n+const Value = 2\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "\ufeffpackage demo\r\n\r\nconst Value = 2\r\n"
	if got := string(prepared.Proposed); got != want {
		t.Fatalf("patch lost BOM or CRLF: got %q, want %q", got, want)
	}
}

func TestPrepareRangeEditPreservesCRLF(t *testing.T) {
	workspace := t.TempDir()
	original := "one\r\ntwo\r\nthree\r\n"
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareOperation(workspace, 0, Operation{
		Operation: "replace_range", Path: "value.txt", BaseSHA256: hashBytes([]byte(original)), RangeStart: 1, RangeEnd: 2, Content: "TWO\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(prepared.Proposed), "one\r\nTWO\r\nthree\r\n"; got != want {
		t.Fatalf("range edit changed line endings: got %q, want %q", got, want)
	}
}

func TestPrepareUpdateNormalizesCRLFContent(t *testing.T) {
	workspace := t.TempDir()
	original := "line one\r\nline two\r\nline three\r\n"
	if err := os.WriteFile(filepath.Join(workspace, "crlf.txt"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	// The caller submits LF-shaped content; the update must preserve the
	// target file's CRLF convention so a one-line edit does not flip the
	// whole file.
	prepared, err := prepareOperation(workspace, 0, Operation{
		Operation: "update", Path: "crlf.txt", ExpectedSHA256: hashBytes([]byte(original)),
		Content: "line one\nline TWO\nline three\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(prepared.Proposed), "line one\r\nline TWO\r\nline three\r\n"; got != want {
		t.Fatalf("update did not preserve CRLF: got %q, want %q", got, want)
	}
}

func TestPrepareUpdateNormalizesMixedContentToLF(t *testing.T) {
	workspace := t.TempDir()
	original := "line one\nline two\nline three\n"
	if err := os.WriteFile(filepath.Join(workspace, "lf.txt"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	// CRLF-shaped content applied to an LF file is reduced to LF.
	prepared, err := prepareOperation(workspace, 0, Operation{
		Operation: "update", Path: "lf.txt", ExpectedSHA256: hashBytes([]byte(original)),
		Content: "line one\r\nline TWO\r\nline three\r\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(prepared.Proposed), "line one\nline TWO\nline three\n"; got != want {
		t.Fatalf("update did not reduce CRLF content to LF: got %q, want %q", got, want)
	}
}

func TestExactEditMatchesCRLFFileWithLFArguments(t *testing.T) {
	workspace := t.TempDir()
	original := "one\r\ntwo\r\nthree\r\n"
	if err := os.WriteFile(filepath.Join(workspace, "crlf.txt"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	hash := hashBytes([]byte(original))

	for _, tc := range []struct {
		name string
		op   Operation
		want string
	}{
		{name: "replace_exact", op: Operation{Operation: "replace_exact", Path: "crlf.txt", BaseSHA256: hash, Match: "two", Content: "TWO"}, want: "one\r\nTWO\r\nthree\r\n"},
		{name: "insert_after", op: Operation{Operation: "insert_after", Path: "crlf.txt", BaseSHA256: hash, Match: "two", Content: "\ninserted"}, want: "one\r\ntwo\r\ninserted\r\nthree\r\n"},
		{name: "insert_before", op: Operation{Operation: "insert_before", Path: "crlf.txt", BaseSHA256: hash, Match: "two", Content: "inserted\n"}, want: "one\r\ninserted\r\ntwo\r\nthree\r\n"},
		{name: "delete_exact", op: Operation{Operation: "delete_exact", Path: "crlf.txt", BaseSHA256: hash, Match: "two\r\n"}, want: "one\r\nthree\r\n"},
		{name: "replace_exact_multiline", op: Operation{Operation: "replace_exact", Path: "crlf.txt", BaseSHA256: hash, Match: "one\r\ntwo", Content: "ONE\r\nTWO"}, want: "ONE\r\nTWO\r\nthree\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := prepareOperation(workspace, 0, tc.op)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(prepared.Proposed); got != tc.want {
				t.Fatalf("exact edit on CRLF file: got %q, want %q", got, tc.want)
			}
		})
	}
}

func assertFile(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("%s: got %q, want %q", path, content, expected)
	}
}
