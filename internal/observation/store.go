package observation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Store persists sanitized observation events in the shared MCPX state DB.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// Append inserts an event and fills its global sequence number.
func (s *Store) Append(ctx context.Context, event Event) (Event, error) {
	if s == nil || s.db == nil {
		return Event{}, fmt.Errorf("observation store database is required")
	}
	if event.Type == "" {
		return Event{}, fmt.Errorf("observation event type is required")
	}
	event.SetDefaults()
	if len(event.Intent) > MaxIntentBytes {
		return Event{}, fmt.Errorf("observation intent exceeds %d bytes", MaxIntentBytes)
	}
	if len(event.Purpose) > MaxIntentBytes {
		return Event{}, fmt.Errorf("observation purpose exceeds %d bytes", MaxIntentBytes)
	}
	if len(event.Goal) > MaxIntentBytes || len(event.ReasoningSummary) > MaxIntentBytes || len(event.NextStep) > MaxIntentBytes || len(event.PlanID) > MaxIntentBytes || len(event.PlanTaskID) > MaxIntentBytes || len(event.ExecutionTaskID) > MaxIntentBytes {
		return Event{}, fmt.Errorf("observation semantic context exceeds %d bytes", MaxIntentBytes)
	}
	if len(event.ProgressSummary) > MaxIntentBytes {
		return Event{}, fmt.Errorf("observation progress summary exceeds %d bytes", MaxIntentBytes)
	}
	if len(event.CallID) > MaxIntentBytes || len(event.TurnID) > MaxIntentBytes || len(event.ActivityKind) > MaxIntentBytes || len(event.RelatedCallID) > MaxIntentBytes {
		return Event{}, fmt.Errorf("observation correlation field exceeds %d bytes", MaxIntentBytes)
	}
	if len(event.Input) > MaxEventBytes || len(event.Output) > MaxEventBytes {
		return Event{}, fmt.Errorf("observation event payload exceeds %d bytes", MaxEventBytes)
	}
	if len(event.Input) > 0 && !json.Valid(event.Input) {
		return Event{}, fmt.Errorf("observation input is not valid JSON")
	}
	if len(event.Output) > 0 && !json.Valid(event.Output) {
		return Event{}, fmt.Errorf("observation output is not valid JSON")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	if event.Input == nil {
		event.Input = json.RawMessage(`{}`)
	}
	if event.Output == nil {
		event.Output = json.RawMessage(`{}`)
	}
	var exitCode any
	if event.ExitCode != nil {
		exitCode = *event.ExitCode
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO observation_events
		 (workspace_name, remote_session_id, request_id, call_id, turn_id, activity_sequence, activity_kind, related_call_id, operation_id, tool_name, event_type, phase,
		 intent, progress_summary, input_json, output_json, summary, status, purpose, goal, reasoning_summary, next_step, plan_id, plan_task_id, execution_task_id, parent_operation_id, step_id,
		 command, working_directory, exit_code, duration_ms, skill_name, mcp_server, mcp_tool, path,
		 resource_uri, stream, stream_offset, truncated, created_at)
		VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)`,
		event.Workspace, event.RemoteSessionID, event.RequestID, event.CallID, event.TurnID, event.ActivitySequence, event.ActivityKind, event.RelatedCallID, event.OperationID, event.Tool,
		event.Type, event.Phase, event.Intent, event.ProgressSummary, string(event.Input), string(event.Output), event.Summary,
		event.Status, event.Purpose, event.Goal, event.ReasoningSummary, event.NextStep, event.PlanID, event.PlanTaskID, event.ExecutionTaskID, event.ParentOperationID, event.StepID, event.Command, event.WorkingDirectory, exitCode,
		event.DurationMs, event.SkillName, event.MCPServer, event.MCPTool, event.Path,
		event.ResourceURI, event.Stream, event.Offset, boolInt(event.Truncated), event.CreatedAt.UnixMilli())
	if err != nil {
		return Event{}, err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	event.Sequence = sequence
	event.setEventID()
	return event, nil
}

// List returns workspace events after the supplied global sequence.
func (s *Store) List(ctx context.Context, workspace string, afterSequence int64, limit int) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("observation store database is required")
	}
	if limit <= 0 {
		limit = DefaultHistory
	}
	if limit > MaxHistory {
		limit = MaxHistory
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, workspace_name,
		remote_session_id, request_id, call_id, turn_id, activity_sequence, activity_kind, related_call_id, operation_id, tool_name, event_type, phase,
		intent, progress_summary, input_json, output_json, summary, status, purpose, goal, reasoning_summary, next_step, plan_id, plan_task_id, execution_task_id, parent_operation_id, step_id,
        command, working_directory, exit_code, duration_ms, skill_name, mcp_server, mcp_tool, path,
        resource_uri, stream, stream_offset, truncated, created_at
        FROM observation_events
        WHERE workspace_name = ? AND sequence > ?
        ORDER BY sequence ASC LIMIT ?`, workspace, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows, limit)
}

// History returns the most recent events for a new observer, or the next
// sequence window when reconnecting after a known sequence.
func (s *Store) History(ctx context.Context, workspace string, afterSequence int64, limit int) ([]Event, error) {
	if afterSequence > 0 {
		return s.List(ctx, workspace, afterSequence, limit)
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("observation store database is required")
	}
	if limit <= 0 {
		limit = DefaultHistory
	}
	if limit > MaxHistory {
		limit = MaxHistory
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, workspace_name,
		remote_session_id, request_id, call_id, turn_id, activity_sequence, activity_kind, related_call_id, operation_id, tool_name, event_type, phase,
	        intent, progress_summary, input_json, output_json, summary, status, purpose, goal, reasoning_summary, next_step, plan_id, plan_task_id, execution_task_id, parent_operation_id, step_id,
        command, working_directory, exit_code, duration_ms, skill_name, mcp_server, mcp_tool, path,
        resource_uri, stream, stream_offset, truncated, created_at
        FROM observation_events
        WHERE workspace_name = ?
        ORDER BY sequence DESC LIMIT ?`, workspace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := scanEvents(rows, limit)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}

func scanEvents(rows *sql.Rows, capacity int) ([]Event, error) {
	events := make([]Event, 0, capacity)
	for rows.Next() {
		var event Event
		var input, output string
		var truncated int
		var exitCode sql.NullInt64
		var createdAt int64
		if err := rows.Scan(&event.Sequence, &event.Workspace, &event.RemoteSessionID,
			&event.RequestID, &event.CallID, &event.TurnID, &event.ActivitySequence, &event.ActivityKind, &event.RelatedCallID, &event.OperationID, &event.Tool, &event.Type, &event.Phase, &event.Intent,
			&event.ProgressSummary, &input, &output, &event.Summary, &event.Status, &event.Purpose, &event.Goal, &event.ReasoningSummary, &event.NextStep, &event.PlanID, &event.PlanTaskID, &event.ExecutionTaskID, &event.ParentOperationID, &event.StepID,
			&event.Command, &event.WorkingDirectory, &exitCode, &event.DurationMs, &event.SkillName, &event.MCPServer, &event.MCPTool, &event.Path,
			&event.ResourceURI, &event.Stream, &event.Offset, &truncated, &createdAt); err != nil {
			return nil, err
		}
		event.Input = json.RawMessage(input)
		event.Output = json.RawMessage(output)
		event.Truncated = truncated != 0
		if exitCode.Valid {
			value := int(exitCode.Int64)
			event.ExitCode = &value
		}
		event.CreatedAt = time.UnixMilli(createdAt).UTC()
		event.SetDefaults()
		event.setEventID()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
