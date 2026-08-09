//go:build !windows

package main

import (
	"reflect"
	"testing"
)

func TestParseDetachedBackgroundProcessesOnlyReturnsMatchingSessionLeaders(t *testing.T) {
	executable := "/opt/mcpx/bin/mcpx"
	output := `  101   101 /opt/mcpx/bin/mcpx -addr 127.0.0.1:9999
  102    77 /opt/mcpx/bin/mcpx -addr 127.0.0.1:9999
  103   103 /opt/other/bin/mcpx -addr 127.0.0.1:9999
  104   104 /opt/mcpx/bin/mcpx-helper
`
	got := parseDetachedBackgroundProcesses(output, executable)
	want := []int{101}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detached daemon pids=%v, want %v", got, want)
	}
}

func TestBackgroundCommandMatchesExactExecutable(t *testing.T) {
	executable := "/opt/mcpx/bin/mcpx"
	for _, command := range []string{
		executable,
		executable + " -addr 127.0.0.1:9999",
	} {
		if !backgroundCommandMatches(command, executable) {
			t.Fatalf("expected command to match: %q", command)
		}
	}
	for _, command := range []string{
		"/opt/other/bin/mcpx -addr 127.0.0.1:9999",
		executable + "-helper",
	} {
		if backgroundCommandMatches(command, executable) {
			t.Fatalf("unexpected command match: %q", command)
		}
	}
}
