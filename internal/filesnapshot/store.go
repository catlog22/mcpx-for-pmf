package filesnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mcpx/internal/file"
)

var ErrNotFound = errors.New("file snapshot not found")

type Store struct{ db *sql.DB }

type persisted struct {
	ID            string            `json:"snapshot_id"`
	At            int64             `json:"at_unix_milli"`
	WorkspaceRoot string            `json:"workspace_root"`
	Hash          map[string]string `json:"hash"`
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Save(ctx context.Context, remoteSessionID string, snapshot file.Snapshot) error {
	payload, err := json.Marshal(persisted{ID: snapshot.ID, At: snapshot.At.UnixMilli(), WorkspaceRoot: snapshot.WorkspaceRoot, Hash: snapshot.Hash})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO file_snapshots(id, remote_session_id, snapshot_json, created_at) VALUES (?, ?, ?, ?)`,
		snapshot.ID, remoteSessionID, string(payload), snapshot.At.UnixMilli())
	if err != nil {
		return fmt.Errorf("save file snapshot: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, remoteSessionID, snapshotID string) (file.Snapshot, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM file_snapshots WHERE id = ? AND remote_session_id = ?`, snapshotID, remoteSessionID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return file.Snapshot{}, ErrNotFound
	}
	if err != nil {
		return file.Snapshot{}, err
	}
	var value persisted
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return file.Snapshot{}, fmt.Errorf("decode file snapshot: %w", err)
	}
	return file.Snapshot{ID: value.ID, At: timeFromMillis(value.At), WorkspaceRoot: value.WorkspaceRoot, Hash: value.Hash}, nil
}

func timeFromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }
