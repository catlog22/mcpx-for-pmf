package config

import (
	"fmt"
	"time"
)

// ValidateRetention validates the effective global retention configuration.
func ValidateRetention(c RetentionConfig) error {
	for name, value := range map[string]string{
		"interval":          c.Interval,
		"process_event_ttl": c.ProcessEventTTL,
		"memory_event_ttl":  c.MemoryEventTTL,
		"terminal_task_ttl": c.TerminalTaskTTL,
		"snapshot_ttl":      c.SnapshotTTL,
	} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			if err == nil {
				err = fmt.Errorf("must be positive")
			}
			return fmt.Errorf("state.retention.%s: %w", name, err)
		}
	}
	for name, value := range map[string]int{
		"process_event_max_rows": c.ProcessEventMaxRows,
		"memory_event_max_rows":  c.MemoryEventMaxRows,
		"vacuum_threshold_rows":  c.VacuumThresholdRows,
	} {
		if value <= 0 {
			return fmt.Errorf("state.retention.%s: must be greater than zero", name)
		}
	}
	return nil
}

// RetentionDurations returns parsed durations after ValidateRetention passes.
func (c RetentionConfig) RetentionDurations() (interval, processEvent, memoryEvent, terminalTask, snapshot time.Duration, err error) {
	if err = ValidateRetention(c); err != nil {
		return 0, 0, 0, 0, 0, err
	}
	interval, _ = time.ParseDuration(c.Interval)
	processEvent, _ = time.ParseDuration(c.ProcessEventTTL)
	memoryEvent, _ = time.ParseDuration(c.MemoryEventTTL)
	terminalTask, _ = time.ParseDuration(c.TerminalTaskTTL)
	snapshot, _ = time.ParseDuration(c.SnapshotTTL)
	return interval, processEvent, memoryEvent, terminalTask, snapshot, nil
}

func mergeRetention(global, overlay RetentionConfig) RetentionConfig {
	out := global
	if overlay.EnabledSet {
		out.Enabled = overlay.Enabled
	}
	if overlay.IntervalSet {
		out.Interval = overlay.Interval
	}
	if overlay.ProcessEventTTLSet {
		out.ProcessEventTTL = overlay.ProcessEventTTL
	}
	if overlay.ProcessEventMaxSet {
		out.ProcessEventMaxRows = overlay.ProcessEventMaxRows
	}
	if overlay.MemoryEventTTLSet {
		out.MemoryEventTTL = overlay.MemoryEventTTL
	}
	if overlay.MemoryEventMaxSet {
		out.MemoryEventMaxRows = overlay.MemoryEventMaxRows
	}
	if overlay.TerminalTaskTTLSet {
		out.TerminalTaskTTL = overlay.TerminalTaskTTL
	}
	if overlay.SnapshotTTLSet {
		out.SnapshotTTL = overlay.SnapshotTTL
	}
	if overlay.VacuumThresholdSet {
		out.VacuumThresholdRows = overlay.VacuumThresholdRows
	}
	return out
}
