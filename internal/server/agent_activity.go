package server

import (
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
	agentActivityProtocolVersion   = "1"
	maxAgentActivityBodyBytes      = 16 << 10
	agentActivityHeartbeatInterval = 25 * time.Second
	agentActivityStateRetention    = time.Hour
	maxTrackedAgentActivityTurns   = 1024
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

var agentActivityStates = func() map[string]bool {
	states := make(map[string]bool, len(agentActivityStateNames))
	for _, state := range agentActivityStateNames {
		states[state] = true
	}
	return states
}()

type agentActivityRequest struct {
	Version         string `json:"version"`
	RemoteSessionID string `json:"remote_session_id"`
	TurnID          string `json:"turn_id"`
	Sequence        int64  `json:"sequence"`
	State           string `json:"state"`
	Summary         string `json:"summary,omitempty"`
	LastCallID      string `json:"last_call_id,omitempty"`
}

type agentActivityState struct {
	RemoteSessionID string
	TurnID          string
	Sequence        int64
	State           string
	Summary         string
	LastCallID      string
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
	if r == nil || r.remote == nil || r.observation == nil {
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

	input.Version = strings.TrimSpace(input.Version)
	input.RemoteSessionID = strings.TrimSpace(input.RemoteSessionID)
	input.TurnID = strings.TrimSpace(input.TurnID)
	input.State = strings.ToLower(strings.TrimSpace(input.State))
	input.Summary = observation.SanitizeIntent(input.Summary)
	input.LastCallID = strings.TrimSpace(input.LastCallID)
	if input.Version != agentActivityProtocolVersion {
		writeAgentActivityError(w, http.StatusUpgradeRequired, "unsupported_protocol_version", "activity version must be "+agentActivityProtocolVersion)
		return
	}
	if input.RemoteSessionID == "" || input.TurnID == "" || input.Sequence <= 0 || !agentActivityStates[input.State] {
		writeAgentActivityError(w, http.StatusBadRequest, "invalid_activity", "remote_session_id, turn_id, positive sequence and a supported state are required")
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

	persisted, reason, err := r.acceptAgentActivity(req, session, input, time.Now().UTC())
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
		"version":           agentActivityProtocolVersion,
		"persisted":         persisted,
		"reason":            reason,
		"remote_session_id": session.ID,
		"workspace":         session.WorkspaceName,
		"turn_id":           input.TurnID,
		"sequence":          input.Sequence,
		"state":             input.State,
	})
}

func (r *Runtime) acceptAgentActivity(req *http.Request, session remotesession.Session, input agentActivityRequest, now time.Time) (bool, string, error) {
	key := input.RemoteSessionID + "\x00" + input.TurnID
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	if r.activityLast == nil {
		r.activityLast = map[string]agentActivityState{}
	}
	r.pruneAgentActivityLocked(now)
	previous, exists := r.activityLast[key]
	if exists {
		if input.Sequence < previous.Sequence {
			return false, "", &agentActivityConflict{Code: "stale_sequence", Message: "activity sequence is older than the latest accepted sequence"}
		}
		if input.Sequence == previous.Sequence {
			if input.State == previous.State && input.Summary == previous.Summary && input.LastCallID == previous.LastCallID {
				return false, "duplicate", nil
			}
			return false, "", &agentActivityConflict{Code: "sequence_conflict", Message: "activity sequence was already used with different content"}
		}
		if previous.State == "turn_completed" || previous.State == "turn_failed" {
			return false, "", &agentActivityConflict{Code: "turn_closed", Message: "terminal activity state already closed this turn"}
		}
	}

	changed := !exists || input.State != previous.State || input.Summary != previous.Summary || input.LastCallID != previous.LastCallID
	persist := changed || previous.PersistedAt.IsZero() || now.Sub(previous.PersistedAt) >= agentActivityHeartbeatInterval
	stateSince := previous.StateSince
	if !exists || input.State != previous.State || stateSince.IsZero() {
		stateSince = now
	}
	state := agentActivityState{
		RemoteSessionID: session.ID,
		TurnID:          input.TurnID,
		Sequence:        input.Sequence,
		State:           input.State,
		Summary:         input.Summary,
		LastCallID:      input.LastCallID,
		PersistedAt:     previous.PersistedAt,
		StateSince:      stateSince,
		SeenAt:          now,
	}
	if persist {
		summary := agentActivitySummary(input.State, input.Summary)
		encoded, _ := json.Marshal(map[string]any{
			"source_type":  "agent.activity.v1",
			"version":      agentActivityProtocolVersion,
			"turn_id":      input.TurnID,
			"sequence":     input.Sequence,
			"state":        input.State,
			"summary":      input.Summary,
			"last_call_id": input.LastCallID,
		})
		if err := r.observation.Record(req.Context(), observation.Event{
			Workspace:       session.WorkspaceName,
			RemoteSessionID: session.ID,
			CallID:          input.LastCallID,
			Type:            observation.TypeObserverNotice,
			Phase:           observation.PhaseThoughtSummary,
			Status:          input.State,
			ProgressSummary: input.Summary,
			Summary:         summary,
			Output:          encoded,
			CreatedAt:       now,
		}); err != nil {
			return false, "", err
		}
		state.PersistedAt = now
	}
	r.activityLast[key] = state
	if persist {
		return true, "state_changed", nil
	}
	return false, "heartbeat_throttled", nil
}

func (r *Runtime) pruneAgentActivityLocked(now time.Time) {
	for key, state := range r.activityLast {
		if now.Sub(state.SeenAt) > agentActivityStateRetention {
			delete(r.activityLast, key)
		}
	}
	if len(r.activityLast) < maxTrackedAgentActivityTurns {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, state := range r.activityLast {
		if oldestKey == "" || state.SeenAt.Before(oldest) {
			oldestKey, oldest = key, state.SeenAt
		}
	}
	if oldestKey != "" {
		delete(r.activityLast, oldestKey)
	}
}

func (r *Runtime) agentActivitySnapshot(remoteSessionID string, now time.Time) map[string]any {
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	if r.activityLast == nil {
		return nil
	}
	r.pruneAgentActivityLocked(now)
	var latest agentActivityState
	found := false
	for _, state := range r.activityLast {
		if state.RemoteSessionID != remoteSessionID {
			continue
		}
		if !found || state.SeenAt.After(latest.SeenAt) {
			latest, found = state, true
		}
	}
	if !found {
		return nil
	}
	return map[string]any{
		"version":      agentActivityProtocolVersion,
		"turn_id":      latest.TurnID,
		"sequence":     latest.Sequence,
		"state":        latest.State,
		"summary":      latest.Summary,
		"last_call_id": latest.LastCallID,
		"state_since":  latest.StateSince.UTC().Format(time.RFC3339Nano),
		"seen_at":      latest.SeenAt.UTC().Format(time.RFC3339Nano),
		"duration_ms":  now.Sub(latest.StateSince).Milliseconds(),
	}
}

func agentActivitySummary(state, summary string) string {
	label := strings.ToUpper(strings.ReplaceAll(state, "_", " "))
	if strings.TrimSpace(summary) == "" {
		return "Agent " + label
	}
	return fmt.Sprintf("Agent %s: %s", label, strings.TrimSpace(summary))
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
