//go:build windows

package server

import "syscall"

// spawnDetachedConfig returns a Windows SysProcAttr that detaches the child:
// DETACHED_PROCESS gives the child no inherited console, and
// CREATE_NEW_PROCESS_GROUP keeps Ctrl+C events scoped to the parent (MCPX).
func spawnDetachedConfig() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}
