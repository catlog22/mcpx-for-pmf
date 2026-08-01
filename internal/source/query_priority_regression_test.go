package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryPrioritizesImplementationFilesForImplementationIntent(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/server/tools_command_execute.go":      "command_execute default wait 10 seconds and task implementation\n",
		"internal/server/tools_change_execute.go":       "change_execute atomic apply and verify implementation\n",
		"internal/server/tools_command_execute_test.go": "command_execute implementation test\n",
		"README.md":                       "command_execute change_execute default wait atomic implementation\n",
		"docs/plans/command-execution.md": "command_execute change_execute default wait atomic implementation\n",
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

	result, err := QueryPage(root, "检查 command_execute 默认等待 10 秒和 change_execute 原子实现", nil, 3, 0, 0, 1<<20, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	filesResult, ok := result["files"].([]map[string]any)
	if !ok || len(filesResult) != 3 {
		t.Fatalf("unexpected query result: %+v", result)
	}
	if filesResult[0]["path"] != "internal/server/tools_change_execute.go" || filesResult[1]["path"] != "internal/server/tools_command_execute.go" {
		t.Fatalf("implementation files were not prioritized: %+v", filesResult)
	}
	for _, item := range filesResult {
		if item["path"] == "README.md" || item["path"] == "docs/plans/command-execution.md" {
			t.Fatalf("low-priority documentation leaked into implementation page: %+v", filesResult)
		}
	}

	scoped, err := QueryPage(root, "command_execute implementation", []string{"internal/server"}, 10, 0, 0, 1<<20, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	scopedFiles, ok := scoped["files"].([]map[string]any)
	if !ok || len(scopedFiles) != 3 {
		t.Fatalf("directory seed was not expanded: %+v", scoped)
	}
	for _, item := range scopedFiles {
		if item["path"] == "internal/server" {
			t.Fatalf("directory was returned as a file: %+v", scopedFiles)
		}
		if !strings.HasPrefix(item["path"].(string), "internal/server/") {
			t.Fatalf("directory scope leaked another path: %+v", scopedFiles)
		}
	}

	goScoped, err := QueryPage(root, "command_execute implementation", []string{"internal/server"}, 10, 0, 0, 1<<20, "", func(path string) bool {
		matched, matchErr := MatchGlob("**/*.go", path)
		return matchErr == nil && matched
	})
	if err != nil {
		t.Fatal(err)
	}
	goFiles, ok := goScoped["files"].([]map[string]any)
	if !ok || len(goFiles) != 3 {
		t.Fatalf("recursive go glob did not match directory files: %+v", goScoped)
	}
}
