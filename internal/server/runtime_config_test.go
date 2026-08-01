package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpx/internal/config"
)

func TestEffectiveConfigInvalidatesProjectCacheOnFileChange(t *testing.T) {
	workspace := t.TempDir()
	runtime := &Runtime{cfg: config.DefaultConfig()}
	projectPath := filepath.Join(workspace, ".mcpx.yaml")

	if got := runtime.effectiveConfig(workspace); !got.Terminal.Enabled {
		t.Fatal("global terminal should remain enabled without project config")
	}
	if err := os.WriteFile(projectPath, []byte("terminal:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runtime.effectiveConfig(workspace); got.Terminal.Enabled {
		t.Fatal("project config was not loaded")
	}
	if got := runtime.effectiveConfig(workspace); got.Terminal.Enabled {
		t.Fatal("cached project config changed unexpectedly")
	}

	if err := os.WriteFile(projectPath, []byte("terminal:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runtime.effectiveConfig(workspace); !got.Terminal.Enabled {
		t.Fatal("project config cache was not invalidated after modification")
	}
}

func TestEffectiveConfigRecoversAfterCachedProjectParseError(t *testing.T) {
	workspace := t.TempDir()
	runtime := &Runtime{cfg: config.DefaultConfig()}
	projectPath := filepath.Join(workspace, ".mcpx.yaml")

	if err := os.WriteFile(projectPath, []byte("terminal: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runtime.effectiveConfig(workspace); got.Terminal.Enabled {
		t.Fatal("invalid project config must fail closed")
	}
	if got := runtime.effectiveConfig(workspace); got.Terminal.Enabled {
		t.Fatal("cached invalid project config must remain fail closed")
	}
	if err := os.WriteFile(projectPath, []byte("terminal:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runtime.effectiveConfig(workspace); !got.Terminal.Enabled {
		t.Fatal("fixed project config did not invalidate cached parse error")
	}
}

func TestFindChangeExecuteRequestPrunesExpiredEntries(t *testing.T) {
	runtime := &Runtime{changeExecuteRequests: map[string]changeExecuteRequest{
		"expired": {changesetID: "old", createdAt: time.Now().UTC().Add(-changeExecuteRequestTTL - time.Minute)},
		"live":    {changesetID: "new", createdAt: time.Now().UTC()},
	}}

	if got, ok := runtime.findChangeExecuteRequest("expired"); ok || got != "" {
		t.Fatalf("expired request returned got=%q ok=%v", got, ok)
	}
	if got, ok := runtime.findChangeExecuteRequest("live"); !ok || got != "new" {
		t.Fatalf("live request missing got=%q ok=%v", got, ok)
	}
	if _, ok := runtime.changeExecuteRequests["expired"]; ok {
		t.Fatal("expired request was not pruned")
	}
}
