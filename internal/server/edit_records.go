package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mcpx/internal/edit"
)

const cleanEditRecordTTL = 24 * time.Hour

type cleanEditRecord struct {
	EditID string           `json:"edit_id"`
	Result edit.BatchResult `json:"result"`
}

func (r *Runtime) saveCleanEditRecord(ctx context.Context, sessionID, principalID, editID, state string, result edit.BatchResult) error {
	if r == nil || r.state == nil || r.state.DB() == nil {
		return fmt.Errorf("state store is required")
	}
	encoded, err := json.Marshal(cleanEditRecord{EditID: editID, Result: result})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = r.state.DB().ExecContext(ctx, `INSERT INTO clean_edit_records
		(id, remote_session_id, principal_id, state, result_json, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, result_json = excluded.result_json,
		updated_at = excluded.updated_at, expires_at = excluded.expires_at`,
		editID, sessionID, principalID, state, string(encoded), now.UnixMilli(), now.UnixMilli(), now.Add(cleanEditRecordTTL).UnixMilli())
	return err
}

func (r *Runtime) loadCleanEditRecord(ctx context.Context, sessionID, principalID, editID string) (cleanEditRecord, string, error) {
	if r == nil || r.state == nil || r.state.DB() == nil {
		return cleanEditRecord{}, "", fmt.Errorf("state store is required")
	}
	var state, encoded string
	err := r.state.DB().QueryRowContext(ctx, `SELECT state, result_json FROM clean_edit_records
		WHERE id = ? AND remote_session_id = ? AND principal_id = ? AND expires_at > ?`,
		editID, sessionID, principalID, time.Now().UTC().UnixMilli()).Scan(&state, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return cleanEditRecord{}, "", fmt.Errorf("edit record %q not found", editID)
	}
	if err != nil {
		return cleanEditRecord{}, "", err
	}
	var record cleanEditRecord
	if err := json.Unmarshal([]byte(encoded), &record); err != nil {
		return cleanEditRecord{}, "", err
	}
	return record, state, nil
}
