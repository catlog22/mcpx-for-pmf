package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryExpandsMultipleDirectorySeedsWithRecursiveGlob(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/server/tools_command_execute.go": "command implementation\n",
		"internal/terminal/task.go":                "task implementation\n",
		"internal/changeset/service.go":            "changeset implementation\n",
		"README.md":                                "implementation documentation\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := QueryPage(root, "检查实现代码", []string{"internal/server", "internal/terminal", "internal/changeset"}, 10, 0, 0, 1<<20, "", func(path string) bool {
		matched, matchErr := MatchGlob("**/*.go", path)
		return matchErr == nil && matched
	})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := result["files"].([]map[string]any)
	if !ok || len(items) != 3 {
		t.Fatalf("multiple directory scope returned %d files: %+v", len(items), result)
	}
	for _, item := range items {
		path, _ := item["path"].(string)
		if !strings.HasSuffix(path, ".go") {
			t.Fatalf("recursive glob returned non-Go file: %+v", items)
		}
		if path == "README.md" {
			t.Fatalf("directory scope returned README: %+v", items)
		}
	}
}
