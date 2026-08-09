package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"mcpx/internal/config"
)

func startBackground(args []string) (int, string, error) {
	layout, err := config.EnsureGlobalLayout()
	if err != nil {
		return 0, "", fmt.Errorf("prepare mcpx home: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return 0, "", fmt.Errorf("resolve executable: %w", err)
	}
	logPath := filepath.Join(layout.LogDir, "mcpx-daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(executable, backgroundChildArgs(args)...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureBackgroundProcess(cmd)
	if err := cmd.Start(); err != nil {
		return 0, "", fmt.Errorf("start child: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, "", fmt.Errorf("release child process: %w", err)
	}
	return pid, logPath, nil
}

func backgroundChildArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-d" {
			continue
		}
		out = append(out, arg)
	}
	return out
}
