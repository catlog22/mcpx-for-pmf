//go:build windows

package environment

func workspaceFreeBytes(_ string) uint64 { return 0 }
