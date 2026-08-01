//go:build !darwin

package main

import "os/exec"

func clipboardCommand(s string) *exec.Cmd {
	return nil
}
