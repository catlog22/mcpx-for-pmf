//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"testing"
)

func testObservationSocketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "mcpx-obs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "observer.sock")
}
