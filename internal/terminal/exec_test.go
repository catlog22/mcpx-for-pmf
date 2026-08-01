package terminal

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecEcho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash -lc path")
	}
	res, err := Exec(context.Background(), ExecOptions{
		WorkDir: t.TempDir(),
		Command: "echo hello-mcpx",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d stderr %s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello-mcpx") {
		t.Fatalf("stdout %q", res.Stdout)
	}
}

func TestExecExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	res, err := Exec(context.Background(), ExecOptions{
		WorkDir: t.TempDir(),
		Command: "exit 7",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("got %d", res.ExitCode)
	}
}
