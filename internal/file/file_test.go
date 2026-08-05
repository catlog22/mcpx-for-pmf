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

func TestReadReportsLineEnding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "crlf.java"), []byte("class Demo {\r\n}\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	read, err := Read(ReadOptions{WorkspaceRoot: root, Path: "crlf.java", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if read.LineEnding != "CRLF" {
		t.Fatalf("line ending = %q, want CRLF", read.LineEnding)
	}

	full, err := ReadFull(FullReadOptions{WorkspaceRoot: root, Path: "crlf.java", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if full.LineEnding != "CRLF" {
		t.Fatalf("full line ending = %q, want CRLF", full.LineEnding)
	}
}

func TestReadFullReturnsWholeTextAndMIME(t *testing.T) {
	root := t.TempDir()
	content := []byte("<!doctype html>\n<html><body>preview</body></html>\n")
	if err := os.WriteFile(filepath.Join(root, "preview.html"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadFull(FullReadOptions{WorkspaceRoot: root, Path: "preview.html", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Content) != string(content) || result.MIMEType != "text/html" || result.Size != int64(len(content)) {
		t.Fatalf("full read=%+v", result)
	}
	if !strings.HasPrefix(result.SHA256, "sha256:") {
		t.Fatalf("missing full-file hash: %q", result.SHA256)
	}
}

func TestReadFullPreservesBinaryAndRejectsOversize(t *testing.T) {
	root := t.TempDir()
	content := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	if err := os.WriteFile(filepath.Join(root, "preview.png"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadFull(FullReadOptions{WorkspaceRoot: root, Path: "preview.png", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Content) != string(content) || result.MIMEType != "image/png" {
		t.Fatalf("binary full read=%+v", result)
	}
	if _, err := ReadFull(FullReadOptions{WorkspaceRoot: root, Path: "preview.png", MaxBytes: 4}); err == nil {
		t.Fatal("oversize full read should fail")
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
