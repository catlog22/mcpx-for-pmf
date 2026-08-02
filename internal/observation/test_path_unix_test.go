//go:build !windows

package observation

import (
	"os"
	"path/filepath"
	"testing"
)

func testObserverPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "mcpx-obs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "observer.sock")
}
