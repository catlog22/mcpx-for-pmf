package observation

import (
	"encoding/json"
	"strconv"
	"time"
)

const (
	MaxIntentBytes = 512
	MaxEventBytes  = 64 << 10
	DefaultHistory = 100
	MaxHistory     = 200
	DefaultBuffer  = 128
)

const (
	TypeToolStarted            = "tool.started"
	TypeToolCompleted          = "tool.completed"
	TypeCommandOutput          = "command.output"
	TypeFileChanged            = "file.changed"
	TypeSessionLifecycle       = "session.lifecycle"
	TypeObserverNotice         = "observer.notice"
	TypeOperationStarted       = "operation.started"
	TypeOperationStepStarted   = "operation.step.started"
	TypeOperationStepCompleted = "operation.step.completed"
	TypeOperationCompleted     = "operation.completed"
)

// Event is the durable, sanitized timeline item consumed by workspace observers.
// Input and Output must already be normalized before they reach Store.Append.
type Event struct {
	Sequence          int64           `json:"sequence"`
	EventID           string          `json:"event_id,omitempty"`
	Workspace         string          `json:"workspace"`
	RemoteSessionID   string          `json:"remote_session_id,omitempty"`
	RequestID         string          `json:"request_id,omitempty"`
	OperationID       string          `json:"operation_id,omitempty"`
	ParentOperationID string          `json:"parent_operation_id,omitempty"`
	StepID            string          `json:"step_id,omitempty"`
	Tool              string          `json:"tool,omitempty"`
	Type              string          `json:"type"`
	Status            string          `json:"status,omitempty"`
	Purpose           string          `json:"purpose,omitempty"`
	Intent            string          `json:"intent,omitempty"`
	ProgressSummary   string          `json:"progress_summary,omitempty"`
	Input             json.RawMessage `json:"input,omitempty"`
	Output            json.RawMessage `json:"output,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	Command           string          `json:"command,omitempty"`
	WorkingDirectory  string          `json:"working_directory,omitempty"`
	ExitCode          *int            `json:"exit_code,omitempty"`
	DurationMs        int64           `json:"duration_ms,omitempty"`
	SkillName         string          `json:"skill_name,omitempty"`
	MCPServer         string          `json:"mcp_server,omitempty"`
	MCPTool           string          `json:"mcp_tool,omitempty"`
	Path              string          `json:"path,omitempty"`
	ResourceURI       string          `json:"resource_uri,omitempty"`
	Stream            string          `json:"stream,omitempty"`
	Offset            int64           `json:"offset,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

func (e *Event) setEventID() {
	if e != nil && e.Sequence > 0 && e.EventID == "" {
		e.EventID = strconv.FormatInt(e.Sequence, 10)
	}
}

// Gap identifies a sequence range that a live subscriber must recover from
// the durable Store before continuing with pushed events.
type Gap struct {
	FromSequence int64 `json:"from_sequence"`
	ToSequence   int64 `json:"to_sequence"`
}
