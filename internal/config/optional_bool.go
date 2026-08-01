package config

import "gopkg.in/yaml.v3"

// The enabled settings need three states while merging configuration:
// absent, explicitly true, and explicitly false. The public bool remains easy
// to consume; EnabledSet records whether YAML contained the field.
func decodeEnabled(node *yaml.Node) (bool, bool, error) {
	var raw struct {
		Enabled *bool `yaml:"enabled"`
	}
	if err := node.Decode(&raw); err != nil {
		return false, false, err
	}
	if raw.Enabled == nil {
		return false, false, nil
	}
	return *raw.Enabled, true, nil
}

func (c *TerminalConfig) UnmarshalYAML(node *yaml.Node) error {
	var err error
	c.Enabled, c.EnabledSet, err = decodeEnabled(node)
	return err
}

func (c *FileWatchConfig) UnmarshalYAML(node *yaml.Node) error {
	var err error
	c.Enabled, c.EnabledSet, err = decodeEnabled(node)
	return err
}

func (c *MCPDiscovery) UnmarshalYAML(node *yaml.Node) error {
	var err error
	c.Enabled, c.EnabledSet, err = decodeEnabled(node)
	return err
}

func (c *SkillsDiscovery) UnmarshalYAML(node *yaml.Node) error {
	type plain SkillsDiscovery
	var value plain
	if err := node.Decode(&value); err != nil {
		return err
	}
	*c = SkillsDiscovery(value)
	var err error
	c.Enabled, c.EnabledSet, err = decodeEnabled(node)
	return err
}

func (c *LoggingConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain LoggingConfig
	var value plain
	if err := node.Decode(&value); err != nil {
		return err
	}
	*c = LoggingConfig(value)
	var err error
	c.Enabled, c.EnabledSet, err = decodeEnabled(node)
	return err
}
