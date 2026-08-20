package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"mcpx/internal/tasks"
)

func putDelegatedTask(t *testing.T, rt *Runtime, taskID, sessionID string, created time.Time) {
	t.Helper()
	delivered := created.Add(time.Second)
	task := tasks.DelegatedTask{
		TaskID:          taskID,
		RemoteSessionID: sessionID,
		Workspace:       "demo",
		TargetOwnerID:   strings.Repeat("1", 32),
		SpawnPID:        4242,
		Action:          "steer",
		Message:         "delegated work " + taskID,
		Purpose:         "verify delegation loop",
		Status:          tasks.StatusDelivered,
		CreatedAt:       created,
		DeliveredAt:     &delivered,
	}
	if err := rt.delegated.Put(task); err != nil {
		t.Fatal(err)
	}
}

func TestTaskResultViewListsAndGetsSessionTasks(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)
	base := time.Now().UTC().Truncate(time.Millisecond)
	firstID, secondID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	putDelegatedTask(t, rt, firstID, remoteID, base)
	putDelegatedTask(t, rt, secondID, remoteID, base.Add(time.Second))

	// List every delegated task recorded for the session.
	listed := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "view", "remote_session_id": remoteID,
	})
	if listed["status"] != "ok" {
		t.Fatalf("list failed: %+v", listed)
	}
	data, _ := listed["data"].(map[string]any)
	if data["remote_session_id"] != remoteID {
		t.Fatalf("list missing remote_session_id: %+v", data)
	}
	if count, _ := data["count"].(float64); count != 2 {
		t.Fatalf("expected 2 tasks, got %+v", data)
	}
	items := asMapSlice(data["tasks"])
	if len(items) != 2 || items[0]["task_id"] != firstID || items[1]["task_id"] != secondID {
		t.Fatalf("expected oldest-first ordering, got %+v", items)
	}
	if items[0]["status"] != "delivered" || items[0]["result_merged"] != false {
		t.Fatalf("unexpected registry item: %+v", items[0])
	}

	// Get a single task by delegated_task_id.
	single := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "view", "remote_session_id": remoteID, "delegated_task_id": firstID,
	})
	if single["status"] != "ok" {
		t.Fatalf("single view failed: %+v", single)
	}
	task, _ := single["data"].(map[string]any)
	if task["task_id"] != firstID || task["status"] != "delivered" ||
		task["action"] != "steer" || task["target_owner_id"] != strings.Repeat("1", 32) ||
		task["message"] != "delegated work "+firstID || task["result_merged"] != false {
		t.Fatalf("unexpected task view: %+v", task)
	}

	// Unknown delegated_task_id reports a typed error.
	missing := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "view", "remote_session_id": remoteID, "delegated_task_id": strings.Repeat("f", 32),
	})
	if errorCode(missing) != "task_not_found" {
		t.Fatalf("expected task_not_found, got %+v", missing)
	}

	// Unsupported actions are rejected.
	invalid := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "list", "remote_session_id": remoteID,
	})
	if errorCode(invalid) != "invalid_action" {
		t.Fatalf("expected invalid_action, got %+v", invalid)
	}
}

func TestTaskResultViewMergesResultFile(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)
	taskID := strings.Repeat("c", 32)
	putDelegatedTask(t, rt, taskID, remoteID, time.Now().UTC())

	// A result file without an explicit status merges as completed.
	raw, err := json.Marshal(tasks.TaskResult{
		TaskID:        taskID,
		Result:        "all checks green",
		ResultSummary: []string{"build ok", "tests pass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rt.delegated.SessionDir(remoteID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.delegated.ResultPath(remoteID, taskID), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	single := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "view", "remote_session_id": remoteID, "delegated_task_id": taskID,
	})
	if single["status"] != "ok" {
		t.Fatalf("view failed: %+v", single)
	}
	data, _ := single["data"].(map[string]any)
	if data["status"] != "completed" || data["result_merged"] != true ||
		data["result"] != "all checks green" {
		t.Fatalf("result file was not merged: %+v", data)
	}
	summary := asStringSlice(data["result_summary"])
	if len(summary) != 2 || summary[0] != "build ok" {
		t.Fatalf("result_summary mismatch: %+v", data["result_summary"])
	}

	// The merged status also shows up in the session listing.
	listed := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "view", "remote_session_id": remoteID,
	})
	items := asMapSlice(listed["data"].(map[string]any)["tasks"])
	if len(items) != 1 || items[0]["status"] != "completed" || items[0]["result_merged"] != true {
		t.Fatalf("list did not merge result file: %+v", items)
	}

	// A failed result file keeps its own status and error.
	failedRaw, _ := json.Marshal(tasks.TaskResult{TaskID: taskID, Status: "failed", Error: "boom"})
	if err := os.WriteFile(rt.delegated.ResultPath(remoteID, taskID), failedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	failed := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
		"action": "view", "remote_session_id": remoteID, "delegated_task_id": taskID,
	})
	failedData, _ := failed["data"].(map[string]any)
	if failedData["status"] != "failed" || failedData["error"] != "boom" {
		t.Fatalf("failed result file was not honored: %+v", failedData)
	}
}

func TestTaskResultViewAfterPiWindowSend(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)
	withFakePeerRoot(t, func(root string) {
		workspaceID := strings.Repeat("a", 64)
		ownerID := strings.Repeat("1", 32)
		writePiOwnerSnapshot(t, root, ownerID, strings.Repeat("b", 32), workspaceID, time.Now().UnixMilli())

		arguments := map[string]any{
			"action": "send", "remote_session_id": remoteID,
			"window": ownerID, "message": "run the delegated job",
			"wait_time_ms": 200,
		}
		if response := callEnvelope(t, rt.toolPiWindow, context.Background(), arguments); errorCode(response) != "user_confirmation_required" {
			t.Fatalf("expected confirmation gate, got %+v", response)
		}
		confirmed := make(map[string]any, len(arguments)+1)
		for key, value := range arguments {
			confirmed[key] = value
		}
		confirmed["user_confirmed"] = true

		sent := callEnvelope(t, rt.toolPiWindow, context.Background(), confirmed)
		if sent["status"] != "ok" {
			t.Fatalf("send failed: %+v", sent)
		}
		data, _ := sent["data"].(map[string]any)
		taskID, _ := data["task_id"].(string)
		commandID, _ := data["command_id"].(string)
		if taskID == "" || taskID != commandID {
			t.Fatalf("expected task_id to equal command_id in send response, got %+v", data)
		}

		// The registry recorded the delivered task.
		record, err := rt.delegated.Get(remoteID, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status != tasks.StatusDelivered || record.TargetOwnerID != ownerID ||
			record.Action != "steer" || record.Message != "run the delegated job" ||
			record.Workspace != "demo" || record.Purpose != "test operation" ||
			record.DeliveredAt == nil || record.SpawnPID != 1234 {
			t.Fatalf("registry record mismatch: %+v", record)
		}

		// The task is queryable through task_result_view.
		viewed := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
			"action": "view", "remote_session_id": remoteID, "delegated_task_id": taskID,
		})
		if viewed["status"] != "ok" {
			t.Fatalf("view failed: %+v", viewed)
		}
		viewData, _ := viewed["data"].(map[string]any)
		if viewData["task_id"] != taskID || viewData["status"] != "delivered" ||
			viewData["message"] != "run the delegated job" || viewData["result_merged"] != false {
			t.Fatalf("unexpected task view: %+v", viewData)
		}

		// Once the result file lands, the view reports the settled outcome.
		raw, _ := json.Marshal(tasks.TaskResult{TaskID: taskID, Result: "job done", ResultSummary: []string{"delivered and finished"}})
		if err := os.WriteFile(rt.delegated.ResultPath(remoteID, taskID), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		settled := callEnvelope(t, rt.toolTaskView, context.Background(), map[string]any{
			"action": "view", "remote_session_id": remoteID, "delegated_task_id": taskID,
		})
		settledData, _ := settled["data"].(map[string]any)
		if settledData["status"] != "completed" || settledData["result"] != "job done" || settledData["result_merged"] != true {
			t.Fatalf("settled view mismatch: %+v", settledData)
		}
	})
}

func asStringSlice(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
