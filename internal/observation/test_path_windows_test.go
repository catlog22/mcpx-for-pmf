//go:build windows

package observation

import "testing"

func testObserverPath(t *testing.T) string {
	return SocketPath(t.TempDir())
}
