package main

import (
	"runtime/debug"
	"testing"

	buildversion "mcpx/internal/version"
)

func TestResolveBuildProvenancePrefersLinkerValues(t *testing.T) {
	got := resolveBuildProvenance("1.2.3", "release-commit", "2026-08-09T00:00:00Z", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "fallback-commit"},
		{Key: "vcs.modified", Value: "true"},
	})
	if got.Version != "1.2.3" || got.Commit != "release-commit" || got.Date != "2026-08-09T00:00:00Z" {
		t.Fatalf("linker provenance must win: %+v", got)
	}
}

func TestResolveBuildProvenanceFallsBackToVCSRevision(t *testing.T) {
	got := resolveBuildProvenance("", "none", "unknown", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
		{Key: "vcs.modified", Value: "true"},
	})
	if got.Version != buildversion.Current || got.Commit != "0123456789abcdef-dirty" || got.Date != "unknown" {
		t.Fatalf("unexpected VCS fallback provenance: %+v", got)
	}
}

func TestBackgroundChildArgsRemovesDaemonFlag(t *testing.T) {
	got := backgroundChildArgs([]string{"-addr", "127.0.0.1:9999", "-d", "-log-level", "debug"})
	want := []string{"-addr", "127.0.0.1:9999", "-log-level", "debug"}
	if len(got) != len(want) {
		t.Fatalf("unexpected args length: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected arg at %d: got=%q want=%q", i, got[i], want[i])
		}
	}
}
