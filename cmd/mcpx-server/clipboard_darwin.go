//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

func clipboardCommand(s string) *exec.Cmd {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd
}
