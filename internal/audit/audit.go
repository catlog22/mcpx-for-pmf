package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one audit line (never includes secret values).
type Event struct {
	Time            string `json:"time"`
	RequestID       string `json:"request_id"`
	RemoteSessionID string `json:"remote_session_id,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	Tool            string `json:"tool"`
	Command         string `json:"command,omitempty"`
	Status          string `json:"status"`
	HasPassword     bool   `json:"has_password,omitempty"`
	ApprovalID      string `json:"approval_id,omitempty"`
	SecretID        string `json:"secret_id,omitempty"`
	Detail          any    `json:"detail,omitempty"`
}

// Logger appends JSONL audit events.
type Logger struct {
	mu   sync.Mutex
	path string
}

// New creates a logger writing to dir/audit.jsonl.
func New(dir string) (*Logger, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Logger{path: filepath.Join(dir, "audit.jsonl")}, nil
}

// Path returns the log file path.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Log appends one event.
func (l *Logger) Log(ev Event) error {
	if l == nil {
		return nil
	}
	if ev.Time == "" {
		ev.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
