package guidance

import "testing"

func TestLoadAgent(t *testing.T) {
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version == "" || cfg.ChangePayload.Tool != "change" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if len(cfg.ToolRouting["modify_files"]) == 0 {
		t.Fatal("missing modify_files routing")
	}
}
