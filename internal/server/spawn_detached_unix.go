//go:build !windows

package server

import "syscall"

// spawnDetachedConfig returns a POSIX SysProcAttr that starts the child in a
// new session, so it is not in MCPX's process group and is not signalled when
// MCPX receives a terminal signal.
func spawnDetachedConfig() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
