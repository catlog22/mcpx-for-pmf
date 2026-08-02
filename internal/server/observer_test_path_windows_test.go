//go:build windows

package server

import (
	"testing"

	"mcpx/internal/observation"
)

func testObservationSocketPath(t *testing.T) string {
	return observation.SocketPath(t.TempDir())
}
