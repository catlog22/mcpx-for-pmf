//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func configureBackgroundProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

func terminateBackgroundProcess(pid int, executable string, timeout time.Duration) (bool, error) {
	if pid <= 0 || pid == os.Getpid() {
		return false, fmt.Errorf("invalid daemon pid %d", pid)
	}
	alive, matches, err := windowsBackgroundProcessState(pid, executable)
	if err != nil {
		return false, err
	}
	if !alive {
		return false, nil
	}
	if !matches {
		return false, fmt.Errorf("pid %d no longer matches daemon executable %s", pid, executable)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, fmt.Errorf("find daemon pid %d: %w", pid, err)
	}
	if err := process.Kill(); err != nil {
		return false, fmt.Errorf("kill daemon pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, _, err := windowsBackgroundProcessState(pid, executable)
		if err != nil {
			return false, err
		}
		if !alive {
			return true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, fmt.Errorf("daemon pid %d did not exit after kill", pid)
}

func discoverBackgroundProcesses(executable string) ([]int, error) {
	return nil, nil
}

func windowsBackgroundProcessState(pid int, executable string) (bool, bool, error) {
	output, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false, false, err
	}
	// CSV format: "Image","PID","Session Name","Session#","Mem Usage".
	// Localized Windows emits a translated info line when nothing matches
	// (e.g. zh-CN “信息: …”), so only an exact PID-column hit counts as
	// alive — same contract as the TUI tunnel alive check.
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		if strings.Trim(fields[1], "\" ") == strconv.Itoa(pid) {
			image := strings.Trim(fields[0], "\" ")
			return true, strings.EqualFold(image, filepath.Base(executable)), nil
		}
	}
	return false, false, nil
}
