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

func (c *RetentionConfig) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		Enabled             *bool   `yaml:"enabled"`
		Interval            *string `yaml:"interval"`
		ProcessEventTTL     *string `yaml:"process_event_ttl"`
		ProcessEventMaxRows *int    `yaml:"process_event_max_rows"`
		MemoryEventTTL      *string `yaml:"memory_event_ttl"`
		MemoryEventMaxRows  *int    `yaml:"memory_event_max_rows"`
		TerminalTaskTTL     *string `yaml:"terminal_task_ttl"`
		SnapshotTTL         *string `yaml:"snapshot_ttl"`
		VacuumThresholdRows *int    `yaml:"vacuum_threshold_rows"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.Enabled != nil {
		c.Enabled, c.EnabledSet = *raw.Enabled, true
	}
	if raw.Interval != nil {
		c.Interval, c.IntervalSet = *raw.Interval, true
	}
	if raw.ProcessEventTTL != nil {
		c.ProcessEventTTL, c.ProcessEventTTLSet = *raw.ProcessEventTTL, true
	}
	if raw.ProcessEventMaxRows != nil {
		c.ProcessEventMaxRows, c.ProcessEventMaxSet = *raw.ProcessEventMaxRows, true
	}
	if raw.MemoryEventTTL != nil {
		c.MemoryEventTTL, c.MemoryEventTTLSet = *raw.MemoryEventTTL, true
	}
	if raw.MemoryEventMaxRows != nil {
		c.MemoryEventMaxRows, c.MemoryEventMaxSet = *raw.MemoryEventMaxRows, true
	}
	if raw.TerminalTaskTTL != nil {
		c.TerminalTaskTTL, c.TerminalTaskTTLSet = *raw.TerminalTaskTTL, true
	}
	if raw.SnapshotTTL != nil {
		c.SnapshotTTL, c.SnapshotTTLSet = *raw.SnapshotTTL, true
	}
	if raw.VacuumThresholdRows != nil {
		c.VacuumThresholdRows, c.VacuumThresholdSet = *raw.VacuumThresholdRows, true
	}
	return nil
}
