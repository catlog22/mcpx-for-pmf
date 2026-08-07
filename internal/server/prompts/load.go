package prompts

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed tools.yaml
var toolsYAML []byte

type fileShape struct {
	Tools map[string]string `yaml:"tools"`
}

// Descriptions returns tool name → short description from embedded tools.yaml.
func Descriptions() (map[string]string, error) {
	var shape fileShape
	if err := yaml.Unmarshal(toolsYAML, &shape); err != nil {
		return nil, fmt.Errorf("prompts tools.yaml: %w", err)
	}
	if len(shape.Tools) == 0 {
		return nil, fmt.Errorf("prompts tools.yaml: empty tools map")
	}
	for name, desc := range shape.Tools {
		if name == "" || desc == "" {
			return nil, fmt.Errorf("prompts tools.yaml: empty name or description")
		}
	}
	return shape.Tools, nil
}

// MustDescriptions panics if embed is invalid.
func MustDescriptions() map[string]string {
	m, err := Descriptions()
	if err != nil {
		panic(err)
	}
	return m
}

// Description returns the short description for a tool, or fallback if missing.
func Description(name, fallback string) string {
	m, err := Descriptions()
	if err != nil {
		return fallback
	}
	if desc, ok := m[name]; ok && desc != "" {
		return desc
	}
	return fallback
}
