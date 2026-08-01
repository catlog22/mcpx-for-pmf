package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListSearchReadAndPolicyFilter(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("do-not-search\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := func(path string) bool { return path != "secret.txt" }
	listed, err := List(root, "", "", 1, true, allowed)
	if err != nil || len(listed.Files) != 1 || listed.Files[0].Path != "src/main.go" || listed.Files[0].SHA256 == "" {
		t.Fatalf("list: %+v err=%v", listed, err)
	}
	searched, err := Search(root, `func\s+main`, "", "", true, 10, allowed)
	if err != nil || len(searched.Matches) != 1 || searched.Matches[0].Line != 3 {
		t.Fatalf("search: %+v err=%v", searched, err)
	}
	secret, err := Search(root, "do-not-search", "", "", false, 10, allowed)
	if err != nil || len(secret.Matches) != 0 {
		t.Fatalf("policy-filtered search leaked: %+v err=%v", secret, err)
	}
	read, err := Read(root, "src/main.go", 0, 2, 1<<20)
	if err != nil || read.SHA256 == "" || !strings.Contains(read.Content, "package main") || !read.Truncated {
		t.Fatalf("read: %+v err=%v", read, err)
	}
}
