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

func TestDiscardDraftLeavesAuditableNonApplyingHistory(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	service := NewService(store.DB())
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "abandoned edit", []Operation{{
		Operation: "create", Path: "abandoned.txt", Content: "draft\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	discarded, err := service.Discard(context.Background(), prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Status != "discarded" || discarded.DiscardedAt == nil {
		t.Fatalf("discarded changeset = %+v", discarded)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); !errors.Is(err, ErrDiscarded) {
		t.Fatalf("discarded changeset apply error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "abandoned.txt")); !os.IsNotExist(err) {
		t.Fatalf("discard must not write workspace file: %v", err)
	}
	history, err := service.History(context.Background(), remoteID, 10)
	if err != nil || len(history) != 1 || history[0].Status != "discarded" {
		t.Fatalf("discarded history = %+v, err=%v", history, err)
	}
}

func TestPrepareAppliesChainedExactEditsForSamePath(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	original := []byte("one\ntwo\nthree\n")
	if err := os.WriteFile(filepath.Join(workspace, "multi.txt"), original, 0o640); err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	base := hashBytes(original)
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "chained exact edits", []Operation{
		{Operation: "replace_exact", Path: "multi.txt", BaseSHA256: base, Match: "one", Content: "ONE"},
		{Operation: "replace_exact", Path: "multi.txt", BaseSHA256: base, Match: "three", Content: "THREE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Files) != 2 {
		t.Fatalf("chained files=%d want 2", len(prepared.Files))
	}
	if got, want := string(prepared.Files[0].Proposed), "ONE\ntwo\nthree\n"; got != want {
		t.Fatalf("first edit proposed=%q want %q", got, want)
	}
	if got := string(prepared.Files[1].Original); got != string(prepared.Files[0].Proposed) {
		t.Fatalf("second edit must chain from first proposed: %q", got)
	}
	if got, want := string(prepared.Files[1].Proposed), "ONE\ntwo\nTHREE\n"; got != want {
		t.Fatalf("second edit proposed=%q want %q", got, want)
	}
	applied, err := service.Apply(context.Background(), prepared.ID, workspace)
	if err != nil || applied.Status != "applied" {
		t.Fatalf("apply chained edits: %+v err=%v", applied, err)
	}
	assertFile(t, filepath.Join(workspace, "multi.txt"), "ONE\ntwo\nTHREE\n")

	revert, err := service.PrepareRevert(context.Background(), prepared.ID, principal.ID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), revert.ID, workspace); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(workspace, "multi.txt"), string(original))
}

// Regression: models reuse source_read.sha256 on every chained exact op. After hop 1,
// intermediate OriginalSHA256 is no longer the disk root; ops 3+ must still accept the root base.
func TestPrepareChainedExactEditsAcceptDiskBaseOnThirdOp(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	original := []byte("alpha\nbeta\ngamma\ndelta\n")
	if err := os.WriteFile(filepath.Join(workspace, "chain.txt"), original, 0o640); err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	base := hashBytes(original)
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "three-hop chain with shared base", []Operation{
		{Operation: "replace_exact", Path: "chain.txt", BaseSHA256: base, Match: "alpha", Content: "ALPHA"},
		{Operation: "replace_exact", Path: "chain.txt", BaseSHA256: base, Match: "beta", Content: "BETA"},
		{Operation: "replace_exact", Path: "chain.txt", BaseSHA256: base, Match: "gamma", Content: "GAMMA"},
	})
	if err != nil {
		t.Fatalf("third chained op must accept on-disk base_sha256: %v", err)
	}
	if len(prepared.Files) != 3 {
		t.Fatalf("files=%d want 3", len(prepared.Files))
	}
	if got, want := string(prepared.Files[2].Proposed), "ALPHA\nBETA\nGAMMA\ndelta\n"; got != want {
		t.Fatalf("proposed=%q want %q", got, want)
	}
	if prepared.Files[0].ChainRootSHA256 != base || prepared.Files[2].ChainRootSHA256 != base {
		t.Fatalf("chain root must stay on-disk base: first=%q third=%q want %q",
			prepared.Files[0].ChainRootSHA256, prepared.Files[2].ChainRootSHA256, base)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(workspace, "chain.txt"), "ALPHA\nBETA\nGAMMA\ndelta\n")
}

func TestApplyCreatesNestedParentDirectories(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	service := NewService(store.DB())
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "create nested files", []Operation{
		{Operation: "create", Path: "src/demo_taskboard/__init__.py", Content: "\n"},
		{Operation: "create", Path: "src/demo_taskboard/core.py", Content: "class Board:\n    pass\n"},
		{Operation: "create", Path: "tests/test_core.py", Content: "import unittest\n"},
		{Operation: "create", Path: "web/index.html", Content: "<!doctype html>\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); err != nil {
		t.Fatal(err)
	}
	for path := range map[string]string{
		"src/demo_taskboard/__init__.py": "\n",
		"src/demo_taskboard/core.py":     "class Board:\n    pass\n",
		"tests/test_core.py":             "import unittest\n",
		"web/index.html":                 "<!doctype html>\n",
	} {
		if _, err := os.Stat(filepath.Join(workspace, path)); err != nil {
			t.Fatalf("nested file %s was not created: %v", path, err)
		}
	}
}

func TestApplyDeletesDirectoryAsOneQuarantinedChange(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	directory := filepath.Join(workspace, "old-project")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "value.txt"), []byte("value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := NewService(store.DB())
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "remove old project", []Operation{{
		Operation: "delete", Path: "old-project",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !IsDirectoryChange(prepared.Files[0]) || !strings.Contains(prepared.UnifiedDiff, "directory removed") {
		t.Fatalf("directory delete was not represented safely: %+v\n%s", prepared.Files[0], prepared.UnifiedDiff)
	}
	if prepared.Files[0].DeletedFiles != 2 || prepared.Files[0].DeletedDirs != 2 {
		t.Fatalf("directory delete counts = files:%d dirs:%d, want files:2 dirs:2", prepared.Files[0].DeletedFiles, prepared.Files[0].DeletedDirs)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("directory was not removed: %v", err)
	}
	backup, err := directoryDeleteBackupPath(workspace, prepared.ID, prepared.Files[0].Ordinal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backup, "nested", "value.txt")); err != nil {
		t.Fatalf("quarantine backup is incomplete: %v", err)
	}
	loaded, err := service.Get(context.Background(), prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Files[0].DeletedFiles != 2 || loaded.Files[0].DeletedDirs != 2 {
		t.Fatalf("persisted directory delete counts = files:%d dirs:%d, want files:2 dirs:2", loaded.Files[0].DeletedFiles, loaded.Files[0].DeletedDirs)
	}
	if _, err := service.PrepareRevert(context.Background(), prepared.ID, principal.ID, workspace); err == nil || !strings.Contains(err.Error(), "cannot be reverted automatically") {
		t.Fatalf("directory revert should report its explicit boundary: %v", err)
	}
}

func TestApplyRestoresQuarantinedDirectoryWhenLaterOperationFails(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	directory := filepath.Join(workspace, "old-project")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sentinel.txt"), []byte("sentinel-old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	service.beforeApply = func(item FileChange) error {
		if item.Ordinal == 1 {
			return errors.New("injected later failure")
		}
		return nil
	}
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "replace old project", []Operation{
		{Operation: "delete", Path: "old-project"},
		{Operation: "update", Path: "sentinel.txt", ExpectedSHA256: hashBytes([]byte("sentinel-old\n")), Content: "sentinel-new\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), prepared.ID, workspace); err == nil {
		t.Fatal("later operation failure unexpectedly succeeded")
	}
	assertFile(t, filepath.Join(directory, "README.md"), "old\n")
	assertFile(t, filepath.Join(workspace, "sentinel.txt"), "sentinel-old\n")
}

func TestPrepareRejectsDirectoryPathAsRegularFileWhenCreatingDescendant(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, nil)
	service := NewService(store.DB())
	_, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "invalid directory operation", []Operation{
		{Operation: "create", Path: "src/demo_taskboard", Content: ""},
		{Operation: "create", Path: "src/demo_taskboard/core.py", Content: "pass\n"},
	})
	if err == nil || !strings.Contains(err.Error(), "parent directories are created automatically") {
		t.Fatalf("directory operation was not rejected: %v", err)
	}
}

func TestPrepareRequiresSeparateDeleteAndCreateOperations(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{"same.txt": "old\n"})
	service := NewService(store.DB())
	_, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "replace same path", []Operation{
		{Operation: "delete", Path: "same.txt"},
		{Operation: "create", Path: "new.txt", Content: "new\n"},
	})
	if err == nil || !strings.Contains(err.Error(), "delete/create conflict") || !strings.Contains(err.Error(), "separate change_execute calls") {
		t.Fatalf("same-path replacement was not rejected clearly: %v", err)
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

func TestPrepareRejectsPatchWithMismatchedContext(t *testing.T) {
	workspace := t.TempDir()
	original := "one\ntwo\n"
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := prepareOperation(workspace, 0, Operation{
		Operation: "update", Path: "value.txt", BaseSHA256: hashBytes([]byte(original)),
		Patch: "@@ -1,2 +1,2 @@\n one\n-missing\n+three\n",
	}, nil)
	if err == nil {
		t.Fatal("expected patch context mismatch")
	}
	if errors.Is(err, ErrStaleRevision) {
		t.Fatalf("patch context mismatch must not be ErrStaleRevision, got %v", err)
	}
	if !strings.Contains(err.Error(), "does not match") && !strings.Contains(err.Error(), "patch apply failed") {
		t.Fatalf("expected patch apply/context error, got %v", err)
	}
}

func TestPrepareChainedMissingBaseIsRevisionRequired(t *testing.T) {
	workspace := t.TempDir()
	original := "alpha\nbeta\n"
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	base := hashBytes([]byte(original))
	first, err := prepareOperation(workspace, 0, Operation{
		Operation: "replace_exact", Path: "value.txt", BaseSHA256: base,
		Match: "alpha", Content: "ALPHA",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareOperation(workspace, 1, Operation{
		Operation: "replace_exact", Path: "value.txt",
		Match: "beta", Content: "BETA",
	}, &first)
	if err == nil {
		t.Fatal("expected missing base_sha256 error")
	}
	if errors.Is(err, ErrStaleRevision) {
		t.Fatalf("missing base must not be ErrStaleRevision, got %v", err)
	}
	if !strings.Contains(err.Error(), "base_sha256 required") {
		t.Fatalf("expected base_sha256 required, got %v", err)
	}
}

func TestPrepareWrongBaseIsStaleRevision(t *testing.T) {
	workspace := t.TempDir()
	original := "alpha\nbeta\n"
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := prepareOperation(workspace, 0, Operation{
		Operation: "replace_exact", Path: "value.txt",
		BaseSHA256: "sha256:" + strings.Repeat("0", 64),
		Match:      "alpha", Content: "ALPHA",
	}, nil)
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("expected ErrStaleRevision, got %v", err)
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
	}, nil)
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
	}, nil)
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
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(prepared.Proposed), "line one\r\nline TWO\r\nline three\r\n"; got != want {
		t.Fatalf("update did not preserve CRLF: got %q, want %q", got, want)
	}
}

func TestPrepareAcceptsCRLFLineCountChange(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{
		"crlf.txt": "line one\r\nline two\r\n",
	})
	service := NewService(store.DB())
	original := []byte("line one\r\nline two\r\n")
	prepared, err := service.Prepare(context.Background(), remoteID, principal.ID, workspace, "add a line", []Operation{{
		Operation: "update", Path: "crlf.txt", ExpectedSHA256: hashBytes(original), Content: "line one\nline TWO\nline three\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Files) != 1 || !prepared.Files[0].FormatPreserved {
		t.Fatalf("format metadata = %+v", prepared.Files)
	}
	if got, want := string(prepared.Files[0].Proposed), "line one\r\nline TWO\r\nline three\r\n"; got != want {
		t.Fatalf("proposed content = %q, want %q", got, want)
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
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(prepared.Proposed), "line one\nline TWO\nline three\n"; got != want {
		t.Fatalf("update did not reduce CRLF content to LF: got %q, want %q", got, want)
	}
}

func TestPrepareRejectsUnexpectedFormatChange(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{
		"crlf.txt": "line one\r\nline two\r\n",
	})
	service := NewService(store.DB())
	original := []byte("line one\r\nline two\r\n")
	_, err := service.PrepareWithOptions(context.Background(), remoteID, principal.ID, workspace, "change content", []Operation{{
		Operation: "update", Path: "crlf.txt", ExpectedSHA256: hashBytes(original), Content: "line one\nline TWO\n",
	}}, PrepareOptions{
		Transform: func(_ string, content []byte) ([]byte, error) {
			return []byte(strings.ReplaceAll(string(content), "\r\n", "\n")), nil
		},
	})
	if !errors.Is(err, ErrFormatChanged) {
		t.Fatalf("expected format change error, got %v", err)
	}
}

func TestPrepareReportsFormatChangeWhenExplicitlyAllowed(t *testing.T) {
	workspace, store, remoteID, principal := newChangesetFixture(t, map[string]string{
		"crlf.txt": "line one\r\nline two\r\n",
	})
	service := NewService(store.DB())
	original := []byte("line one\r\nline two\r\n")
	prepared, err := service.PrepareWithOptions(context.Background(), remoteID, principal.ID, workspace, "format content", []Operation{{
		Operation: "update", Path: "crlf.txt", ExpectedSHA256: hashBytes(original), Content: "line one\nline TWO\n",
	}}, PrepareOptions{
		AllowFormatChange: true,
		Transform: func(_ string, content []byte) ([]byte, error) {
			return []byte(strings.ReplaceAll(string(content), "\r\n", "\n")), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Files) != 1 || prepared.Files[0].FormatPreserved {
		t.Fatalf("format metadata=%+v", prepared.Files)
	}
	if prepared.Files[0].OriginalFormat.LineEnding != "CRLF" || prepared.Files[0].ProposedFormat.LineEnding != "LF" {
		t.Fatalf("format metadata=%+v", prepared.Files[0])
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
			prepared, err := prepareOperation(workspace, 0, tc.op, nil)
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
