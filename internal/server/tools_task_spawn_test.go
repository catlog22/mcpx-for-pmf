package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mcpx/internal/tasks"
)

// TestTaskSpawnInjectionContract verifies the injected instruction names the
// absolute result path, the task id, the write tool, the result-file JSON
// schema and the post-write exit directive.
func TestTaskSpawnInjectionContract(t *testing.T) {
	taskID := strings.Repeat("a", 32)
	resultPath := "/tmp/home/tasks/delegated/session-1/" + taskID + ".result.json"
	injection := taskSpawnInjection(taskID, resultPath)

	for _, required := range []string{
		resultPath, taskID, "write 工具", "退出",
		`"task_id"`, `"status"`, `"completed"`, `"failed"`,
		`"result"`, `"result_summary"`, `"completed_at"`,
	} {
		if !strings.Contains(injection, required) {
			t.Fatalf("injection missing %q: %s", required, injection)
		}
	}

	// The spawn injection extends the delegated writeback with an exit line.
	if !strings.HasSuffix(injection, "写入结果文件后请直接退出。") {
		t.Fatalf("injection must end with the exit directive: %s", injection)
	}
}

// withFakeSpawnCommand swaps piSpawnCommandBuilder for a helper-process based
// fake that records its working dir and argv into recordFile (via env) and
// exits 0. The fake runs as a child of the current test binary using the
// standard os/exec TestHelperProcess pattern.
func withFakeSpawnCommand(t *testing.T, recordFile string) {
	t.Helper()
	previous := piSpawnCommandBuilder
	piSpawnCommandBuilder = func(prompt string) *exec.Cmd {
		cs := []string{"-test.run=TestTaskSpawnHelperProcess", "--"}
		cs = append(cs, prompt)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"MCPX_TEST_HELPER=1",
			"MCPX_TEST_RECORD_FILE="+recordFile,
		)
		return cmd
	}
	t.Cleanup(func() { piSpawnCommandBuilder = previous })
}

// TestTaskSpawnHelperProcess is a helper process invoked by tests; it is
// never run directly as a test. It records os.Args (the injected prompt) and
// its working directory, then exits 0.
func TestTaskSpawnHelperProcess(t *testing.T) {
	if os.Getenv("MCPX_TEST_HELPER") != "1" {
		t.Skip("helper process only")
	}
	recordFile := os.Getenv("MCPX_TEST_RECORD_FILE")
	if recordFile != "" {
		cwd, _ := os.Getwd()
		_ = os.WriteFile(recordFile, []byte(strings.Join(os.Args, "\n")+"\nCWD="+cwd), 0o600)
	}
	os.Exit(0)
}

// callTaskSpawn invokes toolTaskSpawn and returns the decoded envelope.
func callTaskSpawn(t *testing.T, rt *Runtime, arguments map[string]any) map[string]any {
	t.Helper()
	return callEnvelope(t, rt.toolTaskSpawn, context.Background(), arguments)
}

func TestTaskSpawnSpawnsDetachedProcessAndRegisters(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo", "other")
	_, remoteID := openTestRemote(t, rt)

	// Record the spawn invocation (argv + cwd) into this file so the test can
	// assert the injected message, args and working directory.
	recordDir := t.TempDir()
	recordFile := filepath.Join(recordDir, "spawn-record.txt")
	withFakeSpawnCommand(t, recordFile)

	message := "run the detached job"
	response := callTaskSpawn(t, rt, map[string]any{
		"action":            "spawn",
		"remote_session_id": remoteID,
		"message":           message,
	})
	if response["status"] != "ok" {
		t.Fatalf("task_spawn failed: %+v", response)
	}
	data, _ := response["data"].(map[string]any)
	taskID, _ := data["task_id"].(string)
	resultPath, _ := data["result_path"].(string)
	logPath, _ := data["log_path"].(string)
	pidF, _ := data["pid"].(float64)
	pid := int(pidF)

	if taskID == "" || !piOwnerIDPattern.MatchString(taskID) {
		t.Fatalf("task_id must be a hex32 id, got %q", taskID)
	}
	expectedResult := rt.delegated.ResultPath(remoteID, taskID)
	if resultPath != expectedResult {
		t.Fatalf("result_path mismatch: got %q want %q", resultPath, expectedResult)
	}
	if pid <= 0 {
		t.Fatalf("pid must be positive, got %d", pid)
	}
	if !strings.HasSuffix(logPath, filepath.Join("tasks", "delegated", remoteID, taskID+".log")) {
		t.Fatalf("log_path mismatch: %q", logPath)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	if got := data["status"]; got != tasks.StatusExecuting {
		t.Fatalf("status must be executing, got %v", got)
	}
	if got := data["workspace"]; got != "demo" {
		t.Fatalf("workspace must be the session workspace, got %v", got)
	}

	// Wait briefly for the helper process to write the record file.
	deadline := time.Now().Add(2 * time.Second)
	var record string
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(recordFile); err == nil {
			record = string(raw)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if record == "" {
		t.Fatalf("spawn helper did not record its invocation")
	}
	demoWS, _ := rt.reg.Load().Get("demo")
	demoPath := demoWS.Path
	if !strings.Contains(record, "CWD="+demoPath) &&
		!strings.Contains(record, "CWD="+filepath.ToSlash(demoPath)) {
		t.Fatalf("spawn working dir mismatch in record:\n%s", record)
	}
	if !strings.Contains(record, expectedResult) {
		t.Fatalf("injected message must contain the absolute result path:\n%s", record)
	}
	if !strings.Contains(record, "退出") {
		t.Fatalf("injected message must contain the exit directive:\n%s", record)
	}
	// The original user message must be present at the head of the injected prompt.
	if !strings.Contains(record, message) {
		t.Fatalf("injected message must contain the user message:\n%s", record)
	}

	// The registry must record the spawned task with SpawnPID and executing status.
	recorded, err := rt.delegated.Get(remoteID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Status != tasks.StatusExecuting {
		t.Fatalf("registry status must be executing, got %q", recorded.Status)
	}
	if recorded.Action != "spawn" {
		t.Fatalf("registry action must be spawn, got %q", recorded.Action)
	}
	if recorded.SpawnPID != pid {
		t.Fatalf("registry SpawnPID mismatch: got %d want %d", recorded.SpawnPID, pid)
	}
	if recorded.Message != message {
		t.Fatalf("registry message must be the original user message, got %q", recorded.Message)
	}

	// Once a result file is written, task_result_view merges it — closing the loop.
	resultRaw, _ := json.Marshal(tasks.TaskResult{
		TaskID: taskID, Status: tasks.StatusCompleted,
		Result: "detached job done", ResultSummary: []string{"spawned and finished"},
	})
	if err := os.WriteFile(expectedResult, resultRaw, 0o600); err != nil {
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
		viewData["result"] != "detached job done" || viewData["result_merged"] != true {
		t.Fatalf("settled view mismatch: %+v", viewData)
	}
}

func TestTaskSpawnUsesExplicitWorkspace(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo", "other")
	_, remoteID := openTestRemote(t, rt)

	recordDir := t.TempDir()
	recordFile := filepath.Join(recordDir, "spawn-record.txt")
	withFakeSpawnCommand(t, recordFile)

	response := callTaskSpawn(t, rt, map[string]any{
		"action":            "spawn",
		"remote_session_id": remoteID,
		"message":           "run in other workspace",
		"workspace":         "other",
	})
	if response["status"] != "ok" {
		t.Fatalf("task_spawn with explicit workspace failed: %+v", response)
	}
	deadline := time.Now().Add(2 * time.Second)
	var record string
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(recordFile); err == nil {
			record = string(raw)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if record == "" {
		t.Fatalf("spawn helper did not record its invocation")
	}
	otherWS, _ := rt.reg.Load().Get("other")
	otherPath := otherWS.Path
	if !strings.Contains(record, "CWD="+otherPath) && !strings.Contains(record, "CWD="+filepath.ToSlash(otherPath)) {
		t.Fatalf("spawn must use the explicit workspace dir, expected CWD=%s in:\n%s", otherPath, record)
	}
}

func TestTaskSpawnRejectsInvalidMessage(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)

	// Empty message: the injection alone must not masquerade as a task.
	response := callTaskSpawn(t, rt, map[string]any{
		"action": "spawn", "remote_session_id": remoteID, "message": "   ",
	})
	if errorCode(response) != "bad_request" {
		t.Fatalf("expected bad_request for empty message, got %+v", response)
	}

	// Unknown workspace is rejected.
	response = callTaskSpawn(t, rt, map[string]any{
		"action": "spawn", "remote_session_id": remoteID, "message": "do work", "workspace": "nope",
	})
	if errorCode(response) != "workspace_not_found" {
		t.Fatalf("expected workspace_not_found, got %+v", response)
	}
}

func TestSpawnDetachedConfigIsPlatformScoped(t *testing.T) {
	attr := spawnDetachedConfig()
	if attr == nil {
		t.Fatal("spawnDetachedConfig must not return nil")
	}
	if runtime.GOOS == "windows" {
		// DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		if attr.CreationFlags != 0x00000008|0x00000200 {
			t.Fatalf("windows CreationFlags mismatch: %#x", attr.CreationFlags)
		}
	}
}
