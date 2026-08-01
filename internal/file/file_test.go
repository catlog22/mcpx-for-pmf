package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := Resolve(root, "../x"); err == nil {
		t.Fatal("expected escape error")
	}
	p, err := Resolve(root, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, root) {
		t.Fatal(p)
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Resolve(root, "outside/secret.txt"); err == nil {
		t.Fatal("expected symlink escape error")
	}
}

func TestReadAndPatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("hello world\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Read(ReadOptions{WorkspaceRoot: root, Path: "f.txt", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Content, "hello") {
		t.Fatal(r.Content)
	}
	pr, err := SearchReplace(PatchSearchReplace{
		WorkspaceRoot: root,
		Path:          "f.txt",
		OldString:     "hello world",
		NewString:     "hello mcpx",
	})
	if err != nil || !pr.Changed {
		t.Fatalf("%v %+v", err, pr)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "hello mcpx") {
		t.Fatal(string(b))
	}
}

func TestSearchReplaceUnique(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("aa aa"), 0o644)
	_, err := SearchReplace(PatchSearchReplace{
		WorkspaceRoot: root, Path: "f.txt", OldString: "aa", NewString: "b",
	})
	if err == nil {
		t.Fatal("expected non-unique error")
	}
}

func TestSnapshotDiff(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("1"), 0o644)
	s1, err := TakeSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("2"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "b.txt"), []byte("n"), 0o644)
	s2, err := TakeSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	ch := DiffSnapshots(s1, s2)
	if len(ch) < 1 {
		t.Fatal(ch)
	}
}
