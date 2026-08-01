package projecttask

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	manifest := `{"scripts":{"test":"vitest","dev":"vite"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), []byte("lockfileVersion: 9"), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks := Discover(root)
	if len(tasks) != 2 || tasks[1].Command != "pnpm run test" {
		t.Fatalf("tasks=%+v", tasks)
	}
	diagnostics := ParseDiagnostics("src/main.go:12:4: error: broken\nweb.ts(8,2): warning TS1000", 10)
	if len(diagnostics) != 2 || diagnostics[0].Line != 12 || diagnostics[1].Severity != "warning" {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}
