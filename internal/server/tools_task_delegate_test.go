package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpx/internal/tasks"
)

func TestTaskDelegateInjectionContract(t *testing.T) {
	taskID := strings.Repeat("a", 32)
	resultPath := "/tmp/home/tasks/delegated/session-1/" + taskID + ".result.json"
	injection := taskDelegateInjection(taskID, resultPath)

	// The instruction must name the absolute result path, the task id and the
	// result-file JSON schema (mirrors tasks.TaskResult so task_result_view
	// can merge the file).
	for _, required := range []string{
		resultPath, taskID, "write 工具",
		`"task_id"`, `"status"`, `"completed"`, `"failed"`,
		`"result"`, `"result_summary"`, `"completed_at"`,
	} {
		if !strings.Contains(injection, required) {
			t.Fatalf("injection missing %q: %s", required, injection)
		}
	}

	// The peer transport rejects control characters; the injected block must
	// keep any appended message valid.
	if err := validatePiPeerMessage("run the job" + injection); err != nil {
		t.Fatalf("injection must not break peer message validation: %v", err)
	}
}

func TestTaskDelegateDeliversInjectedResultInstruction(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)
	withFakePeerRoot(t, func(root string) {
		workspaceID := strings.Repeat("a", 64)
		ownerID := strings.Repeat("1", 32)
		writePiOwnerSnapshot(t, root, ownerID, strings.Repeat("b", 32), workspaceID, time.Now().UnixMilli())

		message := "run the delegated job"
		arguments := map[string]any{
			"action": "send", "remote_session_id": remoteID,
			"window": ownerID, "message": message,
			"wait_time_ms": 200,
		}

		// First call hits the confirmation gate; the preview and digest bind
		// the original message, not the per-attempt injected one.
		response := callEnvelope(t, rt.toolTaskDelegate, context.Background(), arguments)
		if errorCode(response) != "user_confirmation_required" {
			t.Fatalf("expected confirmation gate, got %+v", response)
		}
		pendingData, _ := response["data"].(map[string]any)
		if pendingData["message"] != message {
			t.Fatalf("confirmation preview must show the original message, got %+v", pendingData)
		}
		if note, _ := pendingData["note"].(string); note == "" {
			t.Fatalf("confirmation payload must carry the injection note, got %+v", pendingData)
		}

		// The user_confirmed retry reuses the pending approval and delivers.
		confirmed := make(map[string]any, len(arguments)+1)
		for key, value := range arguments {
			confirmed[key] = value
		}
		confirmed["user_confirmed"] = true
		sent := callEnvelope(t, rt.toolTaskDelegate, context.Background(), confirmed)
		if sent["status"] != "ok" {
			t.Fatalf("task_delegate failed: %+v", sent)
		}
		data, _ := sent["data"].(map[string]any)
		taskID, _ := data["task_id"].(string)
		commandID, _ := data["command_id"].(string)
		resultPath, _ := data["result_path"].(string)
		if taskID == "" || taskID != commandID {
			t.Fatalf("task_id must equal command_id, got %+v", data)
		}
		expectedPath := rt.delegated.ResultPath(remoteID, taskID)
		if resultPath != expectedPath {
			t.Fatalf("result_path mismatch: got %q want %q", resultPath, expectedPath)
		}

		// The peer command file carries the injected writeback instruction.
		raw, err := os.ReadFile(filepath.Join(root, "commands", ownerID, commandID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var command piPeerCommand
		if err := json.Unmarshal(raw, &command); err != nil {
			t.Fatal(err)
		}
		if command.CommandID != taskID {
			t.Fatalf("peer command id must equal task_id: %+v", command)
		}
		if !strings.HasPrefix(command.Message, message) ||
			!strings.Contains(command.Message, expectedPath) ||
			!strings.Contains(command.Message, taskID) ||
			!strings.Contains(command.Message, `"result_summary"`) {
			t.Fatalf("injected command message mismatch: %+v", command.Message)
		}

		// The shared flow registered the delivered task under the task id.
		record, err := rt.delegated.Get(remoteID, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status != tasks.StatusDelivered || record.TargetOwnerID != ownerID ||
			record.Action != "steer" || !strings.Contains(record.Message, expectedPath) {
			t.Fatalf("registry record mismatch: %+v", record)
		}

		// Once the agent writes the instructed result file, task_result_view
		// merges it into the delegated task — closing the orchestration loop.
		resultRaw, _ := json.Marshal(tasks.TaskResult{
			TaskID: taskID, Status: tasks.StatusCompleted,
			Result: "job done", ResultSummary: []string{"delivered and finished"},
		})
		if err := os.WriteFile(expectedPath, resultRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		viewed := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
			"action": "view", "remote_session_id": remoteID, "delegated_task_id": taskID,
		})
		if viewed["status"] != "ok" {
			t.Fatalf("view failed: %+v", viewed)
		}
		viewData, _ := viewed["data"].(map[string]any)
		if viewData["task_id"] != taskID || viewData["status"] != "completed" ||
			viewData["result"] != "job done" || viewData["result_merged"] != true {
			t.Fatalf("settled view mismatch: %+v", viewData)
		}
	})
}

func TestTaskDelegateRejectsInvalidMessage(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)

	// Empty message: the injection alone must not masquerade as a task.
	response := callEnvelope(t, rt.toolTaskDelegate, context.Background(), map[string]any{
		"action": "send", "remote_session_id": remoteID,
		"window": strings.Repeat("1", 32), "message": "   ",
	})
	if errorCode(response) != "bad_request" {
		t.Fatalf("expected bad_request for empty message, got %+v", response)
	}

	// Control characters are rejected before injection, same as pi_window.
	response = callEnvelope(t, rt.toolTaskDelegate, context.Background(), map[string]any{
		"action": "send", "remote_session_id": remoteID,
		"window": strings.Repeat("1", 32), "message": "line1\nline2",
	})
	if errorCode(response) != "bad_request" {
		t.Fatalf("expected bad_request for control chars, got %+v", response)
	}
}
