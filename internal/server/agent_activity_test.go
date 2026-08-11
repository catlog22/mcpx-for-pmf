package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/observation"
)

func TestAgentActivityIngressRecordsThoughtSummaryAndThrottlesHeartbeat(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	post := func(sequence int64, state, summary string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"version":           agentActivityProtocolVersion,
			"remote_session_id": remoteID,
			"turn_id":           "turn-1",
			"sequence":          sequence,
			"state":             state,
			"summary":           summary,
		})
		req := httptest.NewRequest(http.MethodPost, "/mcp/activity", bytes.NewReader(body))
		res := httptest.NewRecorder()
		rt.agentActivityHandler().ServeHTTP(res, req)
		if res.Code != http.StatusAccepted {
			t.Fatalf("activity status=%d body=%s", res.Code, res.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	first := post(1, "thinking", "Locating the relevant implementation.")
	if first["persisted"] != true || first["reason"] != "state_changed" {
		t.Fatalf("first activity=%+v", first)
	}
	duplicate := post(1, "thinking", "Locating the relevant implementation.")
	if duplicate["persisted"] != false || duplicate["reason"] != "duplicate" {
		t.Fatalf("identical sequence replay should be idempotent: %+v", duplicate)
	}
	second := post(2, "thinking", "Locating the relevant implementation.")
	if second["persisted"] != false || second["reason"] != "heartbeat_throttled" {
		t.Fatalf("duplicate heartbeat should be throttled: %+v", second)
	}
	third := post(3, "reviewing_result", "Checking the previous tool result.")
	if third["persisted"] != true {
		t.Fatalf("state change should persist: %+v", third)
	}

	status := callEnvelope(t, rt.toolObserveStatus, context.Background(), map[string]any{
		"remote_session_id": remoteID,
	})
	statusData, _ := status["data"].(map[string]any)
	currentActivity, _ := statusData["agent_activity"].(map[string]any)
	if currentActivity["state"] != "reviewing_result" || currentActivity["sequence"] != float64(3) {
		t.Fatalf("current agent activity=%+v status=%+v", currentActivity, status)
	}

	terminal := post(4, "turn_completed", "Response delivered.")
	if terminal["persisted"] != true {
		t.Fatalf("terminal activity=%+v", terminal)
	}

	events, _, err := rt.observation.store.Query(context.Background(), observation.HistoryQuery{
		Workspace: "demo",
		SessionID: remoteID,
		Kinds:     []string{observation.TypeObserverNotice},
		Limit:     20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var activityEvents []observation.Event
	for _, event := range events {
		if event.Phase == observation.PhaseThoughtSummary {
			activityEvents = append(activityEvents, event)
		}
	}
	if len(activityEvents) != 3 {
		t.Fatalf("activity events=%d events=%+v", len(activityEvents), activityEvents)
	}
	byStatus := map[string]observation.Event{}
	for _, event := range activityEvents {
		byStatus[event.Status] = event
	}
	thinking, ok := byStatus["thinking"]
	if !ok || thinking.ProgressSummary == "" {
		t.Fatalf("thinking activity missing or empty: %+v", activityEvents)
	}
	if _, ok := byStatus["reviewing_result"]; !ok {
		t.Fatalf("reviewing_result activity missing: %+v", activityEvents)
	}
	if _, ok := byStatus["turn_completed"]; !ok {
		t.Fatalf("terminal activity missing: %+v", activityEvents)
	}
}

func TestGatewayActivityRouteSharesMCPAuthBoundary(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "activity-secret"
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	activityHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	server := httptest.NewServer(NewGateway(cfg, nil, mcpHandler, activityHandler).Handler())
	defer server.Close()

	unauthorized, err := http.Post(server.URL+"/mcp/activity", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp/activity", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer activity-secret")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("authorized status=%d", response.StatusCode)
	}
}
