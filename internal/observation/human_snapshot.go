package observation

import (
	"encoding/json"
	"strings"
)

// HumanObsSnapshot is the only observation payload for local human terminals.
// It must never carry model structuredContent or full ARC envelopes.
type HumanObsSnapshot struct {
	Tool             string
	Status           string
	Purpose          string
	Intent           string
	ReasoningSummary string
	ProgressSummary  string
	NextStep         string
	PlanID           string
	PlanTaskID       string
	ExecutionTaskID  string
	Summary          string
	Command          string
	WorkingDirectory string
	ExitCode         *int
	DurationMs       int64
	Path             string
	ResourceURI      string
	InputRedacted    json.RawMessage
	Truncated        bool
}

// NormalizeHumanToolOutput serializes a human-only tool completion snapshot.
// Output never includes structured_content / mcpx.result.
func NormalizeHumanToolOutput(snap HumanObsSnapshot, maxBytes int) (json.RawMessage, bool) {
	if maxBytes <= 0 {
		maxBytes = MaxEventBytes
	}
	summary, sumTrunc := SanitizeText(snap.Summary, observationSummaryCap(maxBytes))
	payload := map[string]any{
		"available": true,
		"status":    coalesceObsStatus(snap.Status),
		"summary":   summary,
	}
	for key, value := range map[string]string{
		"purpose":           snap.Purpose,
		"reasoning_summary": snap.ReasoningSummary,
		"progress_summary":  snap.ProgressSummary,
		"next_step":         snap.NextStep,
		"plan_id":           snap.PlanID,
		"plan_task_id":      snap.PlanTaskID,
		"execution_task_id": snap.ExecutionTaskID,
	} {
		if value = strings.TrimSpace(value); value != "" {
			payload[key] = value
		}
	}
	if snap.Command != "" {
		payload["command"] = snap.Command
	}
	if snap.WorkingDirectory != "" {
		payload["working_directory"] = snap.WorkingDirectory
	}
	if snap.ExitCode != nil {
		payload["exit_code"] = *snap.ExitCode
	}
	if snap.DurationMs > 0 {
		payload["duration_ms"] = snap.DurationMs
	}
	if snap.Path != "" {
		payload["path"] = snap.Path
	}
	if snap.ResourceURI != "" {
		payload["resource_uri"] = snap.ResourceURI
	}
	clean, truncated := Sanitize(payload, maxBytes)
	truncated = truncated || sumTrunc || snap.Truncated
	if truncated {
		if asMap, ok := clean.(map[string]any); ok {
			asMap["truncated"] = true
			clean = asMap
		}
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage(`{"available":true,"truncated":true}`), true
	}
	return encoded, truncated
}

func coalesceObsStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "succeeded"
	}
	return status
}

func observationSummaryCap(maxBytes int) int {
	if maxBytes < 1024 {
		return maxBytes
	}
	if maxBytes/4 < observationSummaryMaxBytes {
		return maxBytes / 4
	}
	return observationSummaryMaxBytes
}

const observationSummaryMaxBytes = 8 << 10
