package observation

import "testing"

func TestEventTypeConstantsAreStable(t *testing.T) {
	if TypeToolStarted != "tool.started" || TypeToolCompleted != "tool.completed" || TypeFileChanged != "file.changed" {
		t.Fatalf("unexpected event types: %q %q %q", TypeToolStarted, TypeToolCompleted, TypeFileChanged)
	}
}
