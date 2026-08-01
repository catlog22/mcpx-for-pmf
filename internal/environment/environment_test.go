package environment

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpx/internal/state"
)

func TestInspectDoesNotExposeSensitiveValues(t *testing.T) {
	t.Setenv("MCPX_TEST_SECRET_TOKEN", "value-that-must-not-leak")
	report := Inspect(context.Background(), t.TempDir(), []string{"runtime", "os", "architecture", "execution", "shell", "filesystem"})
	if report.OS == nil || report.OS.Type == "" || report.Architecture == nil || report.Architecture.Process == "" {
		t.Fatalf("missing platform identity: %+v", report)
	}
	if report.OS.DisplayCount != len(report.OS.Displays) {
		t.Fatalf("display count mismatch: %+v", report.OS)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "value-that-must-not-leak") || strings.Contains(text, "MCPX_TEST_SECRET_TOKEN") {
		t.Fatalf("sensitive environment data leaked: %s", text)
	}
	if !strings.Contains(text, "***_TOKEN") {
		t.Fatalf("masked variable category missing: %s", text)
	}
}

func TestSnapshotPersistenceDigestAndComparison(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewService(context.Background(), store.DB())
	if err != nil {
		t.Fatal(err)
	}
	base := Report{
		CapturedAt:   time.Unix(1, 0),
		Runtime:      &RuntimeInfo{MCPXVersion: "0.1.0", GoVersion: "go1", PID: 10},
		OS:           &OSInfo{Type: "linux", KernelRelease: "1"},
		Architecture: &ArchitectureInfo{Process: "amd64", Host: "x86_64", ProcessBits: 64},
		Resources:    &ResourceInfo{LogicalCPUs: 4, MemoryTotalBytes: 100},
	}
	first, err := service.Save(context.Background(), "", base)
	if err != nil {
		t.Fatal(err)
	}
	changedDynamic := base
	changedRuntime := *base.Runtime
	changedRuntime.PID = 999
	changedDynamic.Runtime = &changedRuntime
	changedDynamic.CapturedAt = time.Unix(2, 0)
	changedResources := *base.Resources
	changedResources.MemoryTotalBytes = 200
	changedDynamic.Resources = &changedResources
	second, err := service.Save(context.Background(), "", changedDynamic)
	if err != nil {
		t.Fatal(err)
	}
	if first.StaticDigest != second.StaticDigest {
		t.Fatalf("dynamic values changed static digest: %s != %s", first.StaticDigest, second.StaticDigest)
	}
	loaded, err := service.Get(context.Background(), first.ID)
	if err != nil || loaded.Report.OS.Type != "linux" {
		t.Fatalf("loaded snapshot: %+v err=%v", loaded, err)
	}

	after := base
	changedArchitecture := *base.Architecture
	changedArchitecture.Process = "arm64"
	after.Architecture = &changedArchitecture
	comparison := Compare(first.ID, base, after)
	if comparison.HighestSeverity != "breaking" || len(comparison.Changes) != 1 || comparison.Changes[0].Path != "architecture.process" {
		t.Fatalf("unexpected comparison: %+v", comparison)
	}
}
