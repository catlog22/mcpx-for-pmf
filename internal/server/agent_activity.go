package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
)

const (
	agentActivityProtocolVersion   = "2"
	maxAgentActivityBodyBytes      = 16 << 10
	agentActivityHeartbeatInterval = 25 * time.Second
)

var agentActivityStateNames = []string{
	"turn_started",
	"thinking",
	"preparing_action",
	"waiting_tool",
	"reviewing_result",
	"responding",
	"waiting_user",
	"blocked",
	"turn_completed",
	"turn_failed",
}

var agentActivityKindNames = []string{
	"intent",
	"hypothesis",
	"evidence",
	"conclusion",
	"next",
	"status",
}

var agentActivityStates = stringSet(agentActivityStateNames)
var agentActivityKinds = stringSet(agentActivityKindNames)

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

type agentActivityRequest struct {
	RemoteSessionID string `json:"remote_session_id"`
	TurnID          string `json:"turn_id"`
	Sequence        int64  `json:"sequence"`
	State           string `json:"state"`
	Kind            string `json:"kind"`
	Summary         string `json:"summary"`
	RelatedCallID   string `json:"related_call_id,omitempty"`
}

type agentActivityState struct {
	RemoteSessionID string
	TurnID          string
	Sequence        int64
	State           string
	Kind            string
	Summary         string
	RelatedCallID   string
	PersistedAt     time.Time
	StateSince      time.Time
	SeenAt          time.Time
}

type agentActivityConflict struct {
	Code    string
	Message string
}

func (e *agentActivityConflict) Error() string { return e.Message }

func (r *Runtime) agentActivityHandler() http.Handler {
	return http.HandlerFunc(r.handleAgentActivity)
}

func (r *Runtime) handleAgentActivity(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAgentActivityError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST is required")
		return
	}
	if r == nil || r.remote == nil || r.observation == nil || r.state == nil {
		writeAgentActivityError(w, http.StatusServiceUnavailable, "activity_unavailable", "activity observation is unavailable")
		return
	}

	body := http.MaxBytesReader(w, req.Body, maxAgentActivityBodyBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input agentActivityRequest
	if err := decoder.Decode(&input); err != nil {
		writeAgentActivityError(w, http.StatusBadRequest, "invalid_payload", "activity payload is invalid")
		return
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		writeAgentActivityError(w, http.StatusBadRequest, "invalid_payload", "activity payload must contain one JSON value")
		return
	}

	input.RemoteSessionID = strings.TrimSpace(input.RemoteSessionID)
	input.TurnID = strings.TrimSpace(input.TurnID)
	input.State = strings.ToLower(strings.TrimSpace(input.State))
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Summary = observation.SanitizeIntent(input.Summary)
	input.RelatedCallID = strings.TrimSpace(input.RelatedCallID)
	if input.RemoteSessionID == "" || input.TurnID == "" || input.Sequence <= 0 || input.Summary == "" || !agentActivityStates[input.State] || !agentActivityKinds[input.Kind] {
		writeAgentActivityError(w, http.StatusBadRequest, "invalid_activity", "remote_session_id, turn_id, positive sequence, supported state, supported kind and summary are required")
		return
	}

	principal, err := r.principalFromContext(req.Context())
	if err != nil {
		writeAgentActivityError(w, http.StatusUnauthorized, "unauthorized", "authorization is required")
		return
	}
	session, err := r.remote.Get(req.Context(), principal, input.RemoteSessionID)
	if err != nil {
		status := http.StatusInternalServerError
		code := "remote_session_unavailable"
		switch {
		case errors.Is(err, remotesession.ErrNotFound):
			status, code = http.StatusNotFound, "remote_session_not_found"
		case errors.Is(err, remotesession.ErrForbidden):
			status, code = http.StatusForbidden, "remote_session_forbidden"
		}
		writeAgentActivityError(w, status, code, "remote session is unavailable")
		return
	}

	persisted, reason, err := r.acceptAgentActivity(req.Context(), session, input, time.Now().UTC())
	if err != nil {
		var conflict *agentActivityConflict
		if errors.As(err, &conflict) {
			writeAgentActivityError(w, http.StatusConflict, conflict.Code, conflict.Message)
			return
		}
		writeAgentActivityError(w, http.StatusServiceUnavailable, "activity_unavailable", "activity observation is unavailable")
		return
	}

	writeAgentActivityJSON(w, http.StatusAccepted, map[string]any{
		"status":            "accepted",
		"persisted":         persisted,
		"reason":            reason,
		"remote_session_id": session.ID,
		"workspace":         session.WorkspaceName,
		"turn_id":           input.TurnID,
		"sequence":          input.Sequence,
		"state":             input.State,
		"kind":              input.Kind,
		"related_call_id":   input.RelatedCallID,
	})
}

func (r *Runtime) acceptAgentActivity(ctx context.Context, session remotesession.Session, input agentActivityRequest, now time.Time) (bool, string, error) {
	if r == nil || r.state == nil || r.state.DB() == nil {
		return false, "", fmt.Errorf("activity state store is unavailable")
	}
	r.activityMu.Lock()
	defer r.activityMu.Unlock()

	tx, err := r.state.DB().BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()

	previous, exists, err := loadAgentActivityState(ctx, tx, session.ID, input.TurnID)
	if err != nil {
		return false, "", err
	}
	if exists {
		if input.Sequence < previous.Sequence {
			return false, "", &agentActivityConflict{Code: "stale_sequence", Message: "activity sequence is older than the latest accepted sequence"}
		}
		if input.Sequence == previous.Sequence {
			if input.State == previous.State && input.Kind == previous.Kind && input.Summary == previous.Summary && input.RelatedCallID == previous.RelatedCallID {
				return false, "duplicate", nil
			}
			return false, "", &agentActivityConflict{Code: "sequence_conflict", Message: "activity sequence was already used with different content"}
		}
		if isTerminalAgentActivityState(previous.State) {
			return false, "", &agentActivityConflict{Code: "turn_closed", Message: "terminal activity state already closed this turn"}
		}
	}

	semanticUpdate := !exists || input.State != previous.State || input.Kind != previous.Kind || input.Summary != previous.Summary || input.RelatedCallID != previous.RelatedCallID
	persistTimeline := semanticUpdate || previous.PersistedAt.IsZero() || now.Sub(previous.PersistedAt) >= agentActivityHeartbeatInterval
	stateSince := previous.StateSince
	if !exists || input.State != previous.State || stateSince.IsZero() {
		stateSince = now
	}
	state := agentActivityState{
		RemoteSessionID: session.ID,
		TurnID:          input.TurnID,
		Sequence:        input.Sequence,
		State:           input.State,
		Kind:            input.Kind,
		Summary:         input.Summary,
		RelatedCallID:   input.RelatedCallID,
		PersistedAt:     previous.PersistedAt,
		StateSince:      stateSince,
		SeenAt:          now,
	}
	if persistTimeline {
		state.PersistedAt = now
	}
	if err := saveAgentActivityState(ctx, tx, state); err != nil {
		return false, "", err
	}
	if err := tx.Commit(); err != nil {
		return false, "", err
	}

	if persistTimeline {
		if err := r.observation.Record(ctx, observation.Event{
			Workspace:        session.WorkspaceName,
			RemoteSessionID:  session.ID,
			TurnID:           input.TurnID,
			ActivitySequence: input.Sequence,
			ActivityKind:     input.Kind,
			RelatedCallID:    input.RelatedCallID,
			Type:             observation.TypeAgentActivity,
			Phase:            observation.PhaseThoughtSummary,
			Status:           input.State,
			ProgressSummary:  input.Summary,
			Summary:          input.Summary,
			CreatedAt:        now,
		}); err != nil {
			return false, "", err
		}
		if semanticUpdate {
			return true, "semantic_update", nil
		}
		return true, "heartbeat_interval", nil
	}
	return false, "heartbeat_throttled", nil
}

func loadAgentActivityState(ctx context.Context, tx *sql.Tx, remoteSessionID, turnID string) (agentActivityState, bool, error) {
	var state agentActivityState
	var persistedAt, stateSince, seenAt int64
	err := tx.QueryRowContext(ctx, `SELECT remote_session_id, turn_id, sequence, state, kind, summary, related_call_id, persisted_at, state_since, seen_at
		FROM agent_activity_turns WHERE remote_session_id = ? AND turn_id = ?`, remoteSessionID, turnID).Scan(
		&state.RemoteSessionID, &state.TurnID, &state.Sequence, &state.State, &state.Kind, &state.Summary, &state.RelatedCallID,
		&persistedAt, &stateSince, &seenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentActivityState{}, false, nil
	}
	if err != nil {
		return agentActivityState{}, false, err
	}
	state.PersistedAt = activityTime(persistedAt)
	state.StateSince = activityTime(stateSince)
	state.SeenAt = activityTime(seenAt)
	return state, true, nil
}

func saveAgentActivityState(ctx context.Context, tx *sql.Tx, state agentActivityState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_activity_turns
		(remote_session_id, turn_id, sequence, state, kind, summary, related_call_id, persisted_at, state_since, seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(remote_session_id, turn_id) DO UPDATE SET
			sequence = excluded.sequence,
			state = excluded.state,
			kind = excluded.kind,
			summary = excluded.summary,
			related_call_id = excluded.related_call_id,
			persisted_at = excluded.persisted_at,
			state_since = excluded.state_since,
			seen_at = excluded.seen_at`,
		state.RemoteSessionID, state.TurnID, state.Sequence, state.State, state.Kind, state.Summary, state.RelatedCallID,
		activityMillis(state.PersistedAt), activityMillis(state.StateSince), activityMillis(state.SeenAt),
	)
	return err
}

func (r *Runtime) agentActivitySnapshot(ctx context.Context, remoteSessionID string) (map[string]any, error) {
	if r == nil || r.state == nil || r.state.DB() == nil {
		return nil, nil
	}
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	var state agentActivityState
	err := r.state.DB().QueryRowContext(ctx, `SELECT turn_id, sequence, state, kind, summary, related_call_id
		FROM agent_activity_turns WHERE remote_session_id = ? ORDER BY seen_at DESC, turn_id DESC LIMIT 1`, remoteSessionID).Scan(
		&state.TurnID, &state.Sequence, &state.State, &state.Kind, &state.Summary, &state.RelatedCallID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"turn_id":         state.TurnID,
		"sequence":        state.Sequence,
		"state":           state.State,
		"kind":            state.Kind,
		"summary":         state.Summary,
		"related_call_id": state.RelatedCallID,
	}, nil
}

func isTerminalAgentActivityState(state string) bool {
	return state == "turn_completed" || state == "turn_failed"
}

func activityMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func activityTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func writeAgentActivityError(w http.ResponseWriter, status int, code, message string) {
	writeAgentActivityJSON(w, status, map[string]any{
		"status": "error",
		"error":  map[string]any{"code": code, "message": message},
	})
}

func writeAgentActivityJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values")
}
