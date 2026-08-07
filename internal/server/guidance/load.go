package guidance

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed agent.yaml
var agentYAML []byte

// Config is the agent guidance document loaded from agent.yaml.
type Config struct {
	Version          string `yaml:"version"`
	Priority         string `yaml:"priority"`
	Summary          string `yaml:"summary"`
	Rules            []string
	ResponseContract struct {
		Required       bool     `yaml:"required"`
		BeforeToolCall []string `yaml:"before_tool_call"`
		AfterToolCall  []string `yaml:"after_tool_call"`
		FinalResponse  []string `yaml:"final_response"`
		EvidenceRule   string   `yaml:"evidence_rule"`
	} `yaml:"response_contract"`
	ChangePayload struct {
		Tool           string         `yaml:"tool"`
		Required       []string       `yaml:"required"`
		Confirmation   string         `yaml:"confirmation"`
		OperationsItem map[string]any `yaml:"operations_item"`
		Alternatives   string         `yaml:"alternatives"`
	} `yaml:"change_payload"`
	ToolRouting map[string][]string `yaml:"tool_routing"`
}

// LoadAgent returns guidance from the embedded agent.yaml. Missing required
// fields fail fast so catalog regressions cannot ship empty prompts.
func LoadAgent() (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(agentYAML, &cfg); err != nil {
		return Config{}, fmt.Errorf("guidance agent.yaml: %w", err)
	}
	if cfg.Version == "" || cfg.Summary == "" || len(cfg.Rules) == 0 {
		return Config{}, fmt.Errorf("guidance agent.yaml missing version, summary, or rules")
	}
	if cfg.ChangePayload.Tool == "" || len(cfg.ToolRouting) == 0 {
		return Config{}, fmt.Errorf("guidance agent.yaml missing change_payload.tool or tool_routing")
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
