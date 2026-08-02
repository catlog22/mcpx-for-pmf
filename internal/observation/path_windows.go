//go:build windows

package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// observerSocketPath returns a per-MCPX-home pipe name. Named pipes do not
// live in the file system, so the Unix socket path cannot be reused directly.
func observerSocketPath(home string) string {
	normalized := strings.ToLower(filepath.Clean(home))
	digest := sha256.Sum256([]byte(normalized))
	return `\\.\pipe\mcpx-workspace-observer-` + hex.EncodeToString(digest[:8])
}
