package server

import (
	"testing"

	"mcpx/internal/observation"
)

func TestHistoryEventViewUsesCurrentPublicContextFields(t *testing.T) {
	view := historyEventView(observation.Event{
		RemoteSessionID: "rs_demo",
		Goal:            "internal compatibility value",
		Purpose:         "inspect history",
	})
	if view["remote_session_id"] != "rs_demo" {
		t.Fatalf("remote_session_id=%v", view["remote_session_id"])
	}
	if _, exists := view["session_id"]; exists {
		t.Fatalf("history event must use remote_session_id: %+v", view)
	}
	if _, exists := view["goal"]; exists {
		t.Fatalf("history event must not expose internal goal: %+v", view)
	}
	if view["purpose"] != "inspect history" {
		t.Fatalf("purpose=%v", view["purpose"])
	}
}
