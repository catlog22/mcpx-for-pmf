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

func TestAgentActivityIngressRecordsSemanticUpdatesAndThrottlesHeartbeat(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	post := func(sequence int64, state, kind, summary, relatedCallID string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"remote_session_id": remoteID,
			"turn_id":           "turn-1",
			"sequence":          sequence,
			"state":             state,
			"kind":              kind,
			"summary":           summary,
			"related_call_id":   relatedCallID,
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

	first := post(1, "thinking", "intent", "Locating the relevant implementation.", "")
	if first["persisted"] != true || first["reason"] != "semantic_update" {
		t.Fatalf("first activity=%+v", first)
	}
	if _, exists := first["version"]; exists {
		t.Fatalf("V2 response must not repeat protocol version: %+v", first)
	}
	duplicate := post(1, "thinking", "intent", "Locating the relevant implementation.", "")
	if duplicate["persisted"] != false || duplicate["reason"] != "duplicate" {
		t.Fatalf("identical sequence replay should be idempotent: %+v", duplicate)
	}
	second := post(2, "thinking", "intent", "Locating the relevant implementation.", "")
	if second["persisted"] != false || second["reason"] != "heartbeat_throttled" {
		t.Fatalf("duplicate heartbeat should be throttled: %+v", second)
	}
	third := post(3, "reviewing_result", "evidence", "Confirmed the field exists.", "call-read-1")
	if third["persisted"] != true {
		t.Fatalf("semantic change should persist: %+v", third)
	}
	fourth := post(4, "reviewing_result", "evidence", "Confirmed the field exists.", "call-read-2")
	if fourth["persisted"] != true {
		t.Fatalf("related_call_id change should persist immediately: %+v", fourth)
	}

	status := callEnvelope(t, rt.toolObserveStatus, context.Background(), map[string]any{
		"remote_session_id": remoteID,
	})
	statusData, _ := status["data"].(map[string]any)
	currentActivity, _ := statusData["agent_activity"].(map[string]any)
	if currentActivity["state"] != "reviewing_result" || currentActivity["kind"] != "evidence" || currentActivity["sequence"] != float64(4) || currentActivity["related_call_id"] != "call-read-2" {
		t.Fatalf("current agent activity=%+v status=%+v", currentActivity, status)
	}
	if _, exists := currentActivity["version"]; exists {
		t.Fatalf("observe agent_activity must not expose wire version: %+v", currentActivity)
	}
	if _, exists := currentActivity["last_call_id"]; exists {
		t.Fatalf("observe agent_activity must not expose V1 last_call_id: %+v", currentActivity)
	}

	terminal := post(5, "turn_completed", "conclusion", "Response delivered.", "")
	if terminal["persisted"] != true {
		t.Fatalf("terminal activity=%+v", terminal)
	}

	events, _, err := rt.observation.store.Query(context.Background(), observation.HistoryQuery{
		Workspace: "demo",
		SessionID: remoteID,
		Kinds:     []string{observation.TypeAgentActivity},
		Limit:     20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("activity events=%d events=%+v", len(events), events)
	}
	byActivitySequence := map[int64]observation.Event{}
	for _, event := range events {
		byActivitySequence[event.ActivitySequence] = event
	}
	evidence, ok := byActivitySequence[4]
	if !ok || evidence.ActivityKind != "evidence" || evidence.TurnID != "turn-1" || evidence.RelatedCallID != "call-read-2" || evidence.ProgressSummary == "" {
		t.Fatalf("typed activity event missing fields: %+v", events)
	}
}

func TestAgentActivityRejectsV1ShapeAndInvalidKind(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	postRaw := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/mcp/activity", bytes.NewReader(encoded))
		res := httptest.NewRecorder()
		rt.agentActivityHandler().ServeHTTP(res, req)
		return res
	}

	v1 := postRaw(map[string]any{
		"version": "1", "remote_session_id": remoteID, "turn_id": "turn-1", "sequence": 1,
		"state": "thinking", "kind": "intent", "summary": "Inspect the implementation.",
	})
	if v1.Code != http.StatusBadRequest {
		t.Fatalf("V1 payload should be rejected as unknown shape: status=%d body=%s", v1.Code, v1.Body.String())
	}

	invalidKind := postRaw(map[string]any{
		"remote_session_id": remoteID, "turn_id": "turn-1", "sequence": 1,
		"state": "thinking", "kind": "analysis", "summary": "Inspect the implementation.",
	})
	if invalidKind.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind should be rejected: status=%d body=%s", invalidKind.Code, invalidKind.Body.String())
	}
}

func TestAgentActivitySequenceStateSurvivesRuntimeRestart(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	post := func(runtime *Runtime, sequence int64, state, kind, summary string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"remote_session_id": remoteID,
			"turn_id":           "turn-durable",
			"sequence":          sequence,
			"state":             state,
			"kind":              kind,
			"summary":           summary,
		})
		req := httptest.NewRequest(http.MethodPost, "/mcp/activity", bytes.NewReader(body))
		res := httptest.NewRecorder()
		runtime.agentActivityHandler().ServeHTTP(res, req)
		return res
	}
	assertConflict := func(res *httptest.ResponseRecorder, code string) {
		t.Helper()
		if res.Code != http.StatusConflict {
			t.Fatalf("conflict status=%d body=%s", res.Code, res.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		errorBody, _ := payload["error"].(map[string]any)
		if errorBody["code"] != code {
			t.Fatalf("conflict code=%v want=%s body=%s", errorBody["code"], code, res.Body.String())
		}
	}

	if res := post(rt, 1, "thinking", "intent", "Inspect the implementation."); res.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", res.Code, res.Body.String())
	}
	if res := post(rt, 2, "reviewing_result", "evidence", "Confirmed the implementation path."); res.Code != http.StatusAccepted {
		t.Fatalf("second status=%d body=%s", res.Code, res.Body.String())
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	assertConflict(post(restarted, 1, "thinking", "intent", "Inspect the implementation."), "stale_sequence")
	if res := post(restarted, 3, "turn_completed", "conclusion", "Implementation path confirmed."); res.Code != http.StatusAccepted {
		t.Fatalf("terminal status=%d body=%s", res.Code, res.Body.String())
	}
	assertConflict(post(restarted, 4, "responding", "next", "Continue after terminal."), "turn_closed")
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
