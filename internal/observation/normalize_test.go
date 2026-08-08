package observation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeToolInputRedactsSensitiveArguments(t *testing.T) {
	encoded, truncated := NormalizeToolInput(map[string]any{
		"intent": "inspect configuration",
		"token":  "secret-token",
		"nested": map[string]any{"password": "secret-password", "path": "config.yaml"},
	}, MaxEventBytes)
	if truncated {
		t.Fatal("small input was truncated")
	}
	text := string(encoded)
	if strings.Contains(text, "secret-token") || strings.Contains(text, "secret-password") {
		t.Fatalf("sensitive input leaked: %s", text)
	}
	if !strings.Contains(text, redactedValue) {
		t.Fatalf("redaction marker missing: %s", text)
	}
}

func TestNormalizeHumanToolOutputOmitsStructuredContent(t *testing.T) {
	encoded, truncated := NormalizeHumanToolOutput(HumanObsSnapshot{
		Status:           "succeeded",
		Goal:             "verify output",
		Purpose:          "run the command",
		ReasoningSummary: "the command is the smallest probe",
		ProgressSummary:  "command started",
		NextStep:         "inspect the result",
		PlanID:           "pl_1",
		TaskID:           "pt_1",
		Summary:          "visible output",
		Command:          "echo hi",
	}, MaxEventBytes)
	if truncated {
		t.Fatal("small output was truncated")
	}
	text := string(encoded)
	if strings.Contains(text, "structured_content") || strings.Contains(text, "mcpx.result") {
		t.Fatalf("model structured payload leaked into human observation: %s", text)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["summary"] != "visible output" || payload["command"] != "echo hi" {
		t.Fatalf("human fields missing: %s", text)
	}
	for key, want := range map[string]string{
		"goal": "verify output", "purpose": "run the command", "reasoning_summary": "the command is the smallest probe",
		"progress_summary": "command started", "next_step": "inspect the result", "plan_id": "pl_1", "task_id": "pt_1",
	} {
		if payload[key] != want {
			t.Fatalf("human context[%q]=%v, want %q", key, payload[key], want)
		}
	}
}

func TestNormalizeHumanToolOutputRedactsSecretsInSummaryPath(t *testing.T) {
	// Path/command fields go through Sanitize map redaction when nested keys match.
	encoded, _ := NormalizeHumanToolOutput(HumanObsSnapshot{
		Status:  "failed",
		Summary: "failed with password=should-stay-in-summary-unless-key",
		Path:    "ok.go",
	}, MaxEventBytes)
	if !strings.Contains(string(encoded), "ok.go") {
		t.Fatalf("path missing: %s", encoded)
	}
}
