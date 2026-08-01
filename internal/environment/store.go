package environment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

var ErrSnapshotNotFound = errors.New("environment snapshot not found")

type Service struct {
	db                *sql.DB
	runtimeInstanceID string
	now               func() time.Time
}

type Snapshot struct {
	ID                string    `json:"snapshot_id"`
	RemoteSessionID   string    `json:"remote_session_id,omitempty"`
	RuntimeInstanceID string    `json:"runtime_instance_id"`
	StaticDigest      string    `json:"static_digest"`
	Report            Report    `json:"report"`
	CreatedAt         time.Time `json:"created_at"`
}

type Comparison struct {
	BaseSnapshotID  string   `json:"base_snapshot_id"`
	HighestSeverity string   `json:"highest_severity,omitempty"`
	Changes         []Change `json:"changes"`
}

type Change struct {
	Path     string `json:"path"`
	Before   any    `json:"before,omitempty"`
	After    any    `json:"after,omitempty"`
	Severity string `json:"severity"`
}

func NewService(ctx context.Context, db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("environment database required")
	}
	id, err := randomID("rti_", 12)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO runtime_instances(id, started_at, last_seen_at) VALUES (?, ?, ?)`,
		id, now.UnixMilli(), now.UnixMilli()); err != nil {
		return nil, fmt.Errorf("register runtime instance: %w", err)
	}
	return &Service{db: db, runtimeInstanceID: id, now: time.Now}, nil
}

func (s *Service) Save(ctx context.Context, remoteSessionID string, report Report) (Snapshot, error) {
	id, err := randomID("env_", 16)
	if err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC()
	report.SnapshotID = id
	if report.CapturedAt.IsZero() {
		report.CapturedAt = now
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode environment snapshot: %w", err)
	}
	digest, err := staticDigest(report)
	if err != nil {
		return Snapshot{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO environment_snapshots
        (id, remote_session_id, runtime_instance_id, static_digest, snapshot_json, created_at)
        VALUES (?, ?, ?, ?, ?, ?)`, id, nullable(remoteSessionID), s.runtimeInstanceID, digest, string(encoded), now.UnixMilli())
	if err != nil {
		return Snapshot{}, fmt.Errorf("store environment snapshot: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE runtime_instances SET last_seen_at = ? WHERE id = ?`, now.UnixMilli(), s.runtimeInstanceID)
	return Snapshot{ID: id, RemoteSessionID: remoteSessionID, RuntimeInstanceID: s.runtimeInstanceID, StaticDigest: digest, Report: report, CreatedAt: now}, nil
}

func (s *Service) Get(ctx context.Context, snapshotID string) (Snapshot, error) {
	var snapshot Snapshot
	var remoteSessionID sql.NullString
	var encoded string
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, remote_session_id, runtime_instance_id, static_digest, snapshot_json, created_at
        FROM environment_snapshots WHERE id = ?`, snapshotID).Scan(
		&snapshot.ID, &remoteSessionID, &snapshot.RuntimeInstanceID, &snapshot.StaticDigest, &encoded, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrSnapshotNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.RemoteSessionID = remoteSessionID.String
	snapshot.CreatedAt = time.UnixMilli(createdAt).UTC()
	if err := json.Unmarshal([]byte(encoded), &snapshot.Report); err != nil {
		return Snapshot{}, fmt.Errorf("decode environment snapshot: %w", err)
	}
	return snapshot, nil
}

func Compare(baseSnapshotID string, before, after Report) Comparison {
	beforeValues := flattenReport(before)
	afterValues := flattenReport(after)
	keys := make([]string, 0, len(beforeValues)+len(afterValues))
	seen := map[string]bool{}
	for key := range beforeValues {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range afterValues {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	comparison := Comparison{BaseSnapshotID: baseSnapshotID, Changes: []Change{}}
	for _, key := range keys {
		beforeValue, beforeOK := beforeValues[key]
		afterValue, afterOK := afterValues[key]
		if beforeOK == afterOK && reflect.DeepEqual(beforeValue, afterValue) {
			continue
		}
		severity := changeSeverity(key)
		comparison.Changes = append(comparison.Changes, Change{Path: key, Before: beforeValue, After: afterValue, Severity: severity})
		if severityRank(severity) > severityRank(comparison.HighestSeverity) {
			comparison.HighestSeverity = severity
		}
	}
	return comparison
}

func staticDigest(report Report) (string, error) {
	static := struct {
		Runtime      *RuntimeInfo             `json:"runtime,omitempty"`
		OS           *OSInfo                  `json:"os,omitempty"`
		Architecture *ArchitectureInfo        `json:"architecture,omitempty"`
		Execution    *ExecutionInfo           `json:"execution,omitempty"`
		Shell        *ShellInfo               `json:"shell,omitempty"`
		Filesystem   *FilesystemInfo          `json:"filesystem,omitempty"`
		Toolchains   map[string]ToolchainInfo `json:"toolchains,omitempty"`
	}{
		Runtime: report.Runtime, OS: report.OS, Architecture: report.Architecture,
		Execution: report.Execution, Shell: report.Shell, Filesystem: report.Filesystem,
		Toolchains: report.Toolchains,
	}
	if static.Runtime != nil {
		copy := *static.Runtime
		copy.PID = 0
		static.Runtime = &copy
	}
	encoded, err := json.Marshal(static)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func flattenReport(report Report) map[string]any {
	encoded, _ := json.Marshal(report)
	var value map[string]any
	_ = json.Unmarshal(encoded, &value)
	delete(value, "captured_at")
	delete(value, "snapshot_id")
	delete(value, "comparison")
	flat := map[string]any{}
	flattenMap("", value, flat)
	return flat
}

func flattenMap(prefix string, value map[string]any, result map[string]any) {
	for key, item := range value {
		path := strings.TrimPrefix(prefix+"."+key, ".")
		if child, ok := item.(map[string]any); ok {
			flattenMap(path, child, result)
			continue
		}
		result[path] = item
	}
}

func changeSeverity(path string) string {
	for _, prefix := range []string{
		"os.type", "architecture.process", "architecture.host", "architecture.process_bits",
		"execution.container", "execution.container_type", "execution.wsl", "filesystem.case_sensitivity",
	} {
		if strings.HasPrefix(path, prefix) {
			return "breaking"
		}
	}
	for _, prefix := range []string{"os.", "shell.execution_shell", "runtime.go_version", "toolchains."} {
		if strings.HasPrefix(path, prefix) {
			return "warning"
		}
	}
	return "info"
}

func severityRank(severity string) int {
	switch severity {
	case "breaking":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
