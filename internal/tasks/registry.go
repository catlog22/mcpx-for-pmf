// Package tasks implements the delegated-task registry: a file-backed record
// of tasks dispatched to remote Pi windows/sessions. Entries are plain JSON
// files (one per task) so external readers — e.g. the TS-side TUI and the
// Phase 2 result writers — can inspect delegation state without a database
// driver.
package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Task lifecycle states.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusExecuting = "executing"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// ErrNotFound is returned when a task or result file does not exist.
var ErrNotFound = errors.New("delegated task not found")

// DelegatedTask records one task delegated to a remote Pi window. The JSON
// shape is a public contract: the TUI and result writers read these files
// directly, so field names stay snake_case and stable.
type DelegatedTask struct {
	TaskID          string     `json:"task_id"`
	RemoteSessionID string     `json:"remote_session_id"`
	Workspace       string     `json:"workspace"`
	TargetOwnerID   string     `json:"target_owner_id"`
	SpawnPID        int        `json:"spawn_pid"`
	Action          string     `json:"action"`
	Message         string     `json:"message"`
	Purpose         string     `json:"purpose"`
	Status          string     `json:"status"`
	Result          string     `json:"result"`
	ResultSummary   []string   `json:"result_summary"`
	CreatedAt       time.Time  `json:"created_at"`
	DeliveredAt     *time.Time `json:"delivered_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Error           string     `json:"error"`
}

// TaskResult is the shape of the {taskID}.result.json files written next to
// the registry entry once the delegated work settles (Phase 2 writers).
type TaskResult struct {
	TaskID        string     `json:"task_id"`
	Status        string     `json:"status,omitempty"`
	Result        string     `json:"result,omitempty"`
	ResultSummary []string   `json:"result_summary,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// Registry stores delegated tasks as {root}/{remoteSessionID}/{taskID}.json.
// The root lives under {home}/tasks/delegated so registry files never collide
// with terminal task logs stored directly under {home}/tasks by
// terminal.NewPersistentTaskManager.
type Registry struct {
	root string
	mu   sync.Mutex
}

// NewRegistry builds a registry rooted at {home}/tasks/delegated.
func NewRegistry(home string) *Registry {
	return &Registry{root: filepath.Join(home, "tasks", "delegated")}
}

// Root returns the registry storage root.
func (r *Registry) Root() string { return r.root }

// SessionDir returns the directory holding one session's task files.
func (r *Registry) SessionDir(sessionID string) string {
	return filepath.Join(r.root, sessionID)
}

// TaskPath returns the registry file path for one task.
func (r *Registry) TaskPath(sessionID, taskID string) string {
	return filepath.Join(r.SessionDir(sessionID), taskID+".json")
}

// ResultPath returns the result file path ({taskID}.result.json) for a task.
func (r *Registry) ResultPath(sessionID, taskID string) string {
	return filepath.Join(r.SessionDir(sessionID), taskID+".result.json")
}

// Put writes (or overwrites) a task entry atomically.
func (r *Registry) Put(task DelegatedTask) error {
	if err := validateID("task_id", task.TaskID); err != nil {
		return err
	}
	if err := validateID("remote_session_id", task.RemoteSessionID); err != nil {
		return err
	}
	if strings.TrimSpace(task.Status) == "" {
		return fmt.Errorf("tasks: status is required")
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("tasks: marshal %s: %w", task.TaskID, err)
	}
	dir := r.SessionDir(task.RemoteSessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("tasks: create session dir: %w", err)
	}
	target := r.TaskPath(task.RemoteSessionID, task.TaskID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("tasks: write %s: %w", task.TaskID, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tasks: commit %s: %w", task.TaskID, err)
	}
	return nil
}

// Get loads one task. Missing files yield ErrNotFound.
func (r *Registry) Get(sessionID, taskID string) (DelegatedTask, error) {
	if err := validateID("remote_session_id", sessionID); err != nil {
		return DelegatedTask{}, err
	}
	if err := validateID("task_id", taskID); err != nil {
		return DelegatedTask{}, err
	}
	raw, err := os.ReadFile(r.TaskPath(sessionID, taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return DelegatedTask{}, fmt.Errorf("%w: %s", ErrNotFound, taskID)
		}
		return DelegatedTask{}, fmt.Errorf("tasks: read %s: %w", taskID, err)
	}
	var task DelegatedTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return DelegatedTask{}, fmt.Errorf("tasks: parse %s: %w", taskID, err)
	}
	return task, nil
}

// ListBySession returns all tasks recorded for a session, oldest first.
// Result files ({taskID}.result.json) are not registry entries and are
// skipped; unknown sessions yield an empty list.
func (r *Registry) ListBySession(sessionID string) ([]DelegatedTask, error) {
	if err := validateID("remote_session_id", sessionID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.SessionDir(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return []DelegatedTask{}, nil
		}
		return nil, fmt.Errorf("tasks: list session %s: %w", sessionID, err)
	}
	out := make([]DelegatedTask, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".result.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.SessionDir(sessionID), name))
		if err != nil {
			continue
		}
		var task DelegatedTask
		if json.Unmarshal(raw, &task) != nil {
			continue
		}
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}

// ReadResult loads the {taskID}.result.json companion file. Missing files
// yield ErrNotFound.
func (r *Registry) ReadResult(sessionID, taskID string) (TaskResult, error) {
	if err := validateID("remote_session_id", sessionID); err != nil {
		return TaskResult{}, err
	}
	if err := validateID("task_id", taskID); err != nil {
		return TaskResult{}, err
	}
	raw, err := os.ReadFile(r.ResultPath(sessionID, taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return TaskResult{}, fmt.Errorf("%w: %s", ErrNotFound, taskID)
		}
		return TaskResult{}, fmt.Errorf("tasks: read result %s: %w", taskID, err)
	}
	var result TaskResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return TaskResult{}, fmt.Errorf("tasks: parse result %s: %w", taskID, err)
	}
	return result, nil
}

// validateID keeps caller-supplied identifiers from escaping the registry
// layout; task and session ids are hex tokens in practice.
func validateID(field, id string) error {
	if strings.TrimSpace(id) == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("tasks: invalid %s", field)
	}
	return nil
}
