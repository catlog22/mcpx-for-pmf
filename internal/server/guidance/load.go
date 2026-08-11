package guidance

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed agent.yaml
var agentYAML []byte

// Config is the compact agent guidance document loaded from agent.yaml.
type Config struct {
	Version     string              `yaml:"version"`
	Priority    string              `yaml:"priority"`
	Summary     string              `yaml:"summary"`
	Rules       []string            `yaml:"rules"`
	ToolRouting map[string][]string `yaml:"tool_routing"`
}

// LoadAgent returns the embedded guidance. Tool schemas and Runtime recovery
// are authoritative for protocol details; this document only carries stable
// engineering invariants and routing hints.
func LoadAgent() (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(agentYAML, &cfg); err != nil {
		return Config{}, fmt.Errorf("guidance agent.yaml: %w", err)
	}
	if cfg.Version == "" || cfg.Summary == "" || len(cfg.Rules) == 0 || len(cfg.ToolRouting) == 0 {
		return Config{}, fmt.Errorf("guidance agent.yaml missing version, summary, rules, or tool_routing")
	}
	return cfg, nil
}

// MustLoadAgent panics if embedded guidance is invalid (package init / tests).
func MustLoadAgent() Config {
	cfg, err := LoadAgent()
	if err != nil {
		panic(err)
	}
	return cfg
}
