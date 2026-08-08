package observation

import "testing"

func TestEventTypeConstantsAreStable(t *testing.T) {
	if TypeToolStarted != "tool.started" || TypeToolCompleted != "tool.completed" || TypeFileChanged != "file.changed" {
		t.Fatalf("unexpected event types: %q %q %q", TypeToolStarted, TypeToolCompleted, TypeFileChanged)
	}
	if PhaseForEvent(TypeToolStarted, "started") != PhaseActionStarted || PhaseForEvent(TypeCommandOutput, "running") != PhaseOutput || PhaseForEvent(TypeToolCompleted, "failed") != PhaseError {
		t.Fatalf("unexpected event phases")
	}
	event := Event{RequestID: "req_phase", Type: TypeToolCompleted, Status: "succeeded"}
	event.SetDefaults()
	if event.CallID != event.RequestID || event.Phase != PhaseResult {
		t.Fatalf("defaults=%+v", event)
	}
}
