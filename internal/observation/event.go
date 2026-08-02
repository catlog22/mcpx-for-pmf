package observation

import (
	"encoding/json"
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
	TypeToolStarted      = "tool.started"
	TypeToolCompleted    = "tool.completed"
	TypeCommandOutput    = "command.output"
	TypeFileChanged      = "file.changed"
	TypeSessionLifecycle = "session.lifecycle"
	TypeObserverNotice   = "observer.notice"
)

// Event is the durable, sanitized timeline item consumed by workspace observers.
// Input and Output must already be normalized before they reach Store.Append.
type Event struct {
	Sequence        int64           `json:"sequence"`
	Workspace       string          `json:"workspace"`
	RemoteSessionID string          `json:"remote_session_id,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	OperationID     string          `json:"operation_id,omitempty"`
	Tool            string          `json:"tool,omitempty"`
	Type            string          `json:"type"`
	Intent          string          `json:"intent,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	ResourceURI     string          `json:"resource_uri,omitempty"`
	Stream          string          `json:"stream,omitempty"`
	Offset          int64           `json:"offset,omitempty"`
	Truncated       bool            `json:"truncated,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// Gap identifies a sequence range that a live subscriber must recover from
// the durable Store before continuing with pushed events.
type Gap struct {
	FromSequence int64 `json:"from_sequence"`
	ToSequence   int64 `json:"to_sequence"`
}
