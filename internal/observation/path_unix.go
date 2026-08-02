//go:build !windows

package observation

import "path/filepath"

func observerSocketPath(home string) string {
	return filepath.Join(home, "run", "workspace-observer.sock")
}
