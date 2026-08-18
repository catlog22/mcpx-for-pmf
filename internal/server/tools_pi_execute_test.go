package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/plan"
	"mcpx/internal/remotesession"
)

func openTestRemote(t *testing.T, rt *Runtime) (string, string) {
	t.Helper()
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID, _ := opened["remote_session_id"].(string)
	if remoteID == "" {
		t.Fatalf("open session failed: %+v", opened)
	}
	return principal.ID, remoteID
}

func TestPiExecuteValidation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)

	// prompt required
	response := callEnvelope(t, rt.toolPiExecute, context.Background(), map[string]any{
		"remote_session_id": remoteID, "prompt": "  ",
	})
	if errorCode(response) != "bad_request" {
		t.Fatalf("expected bad_request for missing prompt, got %+v", response)
	}

	// plan_id / plan_task_id must be paired
	response = callEnvelope(t, rt.toolPiExecute, context.Background(), map[string]any{
		"remote_session_id": remoteID, "prompt": "do something", "plan_id": "pl_x",
	})
	if errorCode(response) != "bad_request" {
		t.Fatalf("expected bad_request for unpaired plan_id, got %+v", response)
	}
}

func TestPiExecuteConfirmationRequiredUnderConfirmPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	ws := filepath.Join(home, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "proj", Path: ws}}
	cfg.Logging.Enabled = false
	cfg.Logging.Dir = filepath.Join(home, "logs")
	cfg.Security.Commands.Default = "confirm"
	cfg.Security.Commands.Allow = nil
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "proj", WorkspacePath: ws, Label: "confirm test",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := callEnvelope(t, rt.toolPiExecute, context.Background(), map[string]any{
		"remote_session_id": created.Session.ID, "prompt": "run a quick check",
	})
	if errorCode(response) != "user_confirmation_required" {
		t.Fatalf("expected user_confirmation_required under confirm policy, got %+v", response)
	}
	if data, _ := response["data"].(map[string]any); data == nil || data["confirmation_required"] != true {
		t.Fatalf("expected confirmation data, got %+v", response)
	}
}

func TestPiExecuteDeniedByPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	ws := filepath.Join(home, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "proj", Path: ws}}
	cfg.Logging.Enabled = false
	cfg.Logging.Dir = filepath.Join(home, "logs")
	cfg.Security.Commands.Deny = []string{`(?i)^pi\b`}
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "proj", WorkspacePath: ws, Label: "deny test",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := callEnvelope(t, rt.toolPiExecute, context.Background(), map[string]any{
		"remote_session_id": created.Session.ID, "prompt": "run a quick check",
	})
	if errorCode(response) != "denied" {
		t.Fatalf("expected denied by policy, got %+v", response)
	}
}

func TestEnsurePlanTaskInProgress(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principalID, remoteID := openTestRemote(t, rt)
	ctx := context.Background()

	created, err := rt.plans.Create(ctx, remoteID, principalID, plan.CreateInput{
		Goal: "g", Tasks: []plan.TaskInput{{Title: "t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := created.Tasks[0].ID

	started, err := rt.ensurePlanTaskInProgress(ctx, remoteID, principalID, created.ID, taskID)
	if err != nil || started.Status != plan.TaskInProgress {
		t.Fatalf("start task: task=%+v err=%v", started, err)
	}
	reused, err := rt.ensurePlanTaskInProgress(ctx, remoteID, principalID, created.ID, taskID)
	if err != nil || reused.Status != plan.TaskInProgress {
		t.Fatalf("reuse in_progress task: task=%+v err=%v", reused, err)
	}
	if _, err := rt.ensurePlanTaskInProgress(ctx, remoteID, principalID, created.ID, "pl_task_missing"); err == nil {
		t.Fatal("expected error for unknown task")
	}
	if _, err := rt.plans.CompleteTask(ctx, remoteID, created.ID, taskID, principalID, []plan.EvidenceInput{
		{Kind: plan.EvidenceExecute, ReferenceID: "task-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ensurePlanTaskInProgress(ctx, remoteID, principalID, created.ID, taskID); err == nil {
		t.Fatal("expected invalid state error for completed task")
	}
}

func TestClosePlanTaskAfterPi(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principalID, remoteID := openTestRemote(t, rt)
	ctx := context.Background()

	// success: exit 0 -> completed with execute evidence
	created, err := rt.plans.Create(ctx, remoteID, principalID, plan.CreateInput{
		Goal: "g", Tasks: []plan.TaskInput{{Title: "t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := created.Tasks[0].ID
	if _, err := rt.ensurePlanTaskInProgress(ctx, remoteID, principalID, created.ID, taskID); err != nil {
		t.Fatal(err)
	}
	closed, err := rt.closePlanTaskAfterPi(ctx, remoteID, principalID, created.ID, taskID, "exec-1", map[string]any{"exit_code": 0})
	if err != nil || closed.Status != plan.TaskCompleted {
		t.Fatalf("close success: task=%+v err=%v", closed, err)
	}
	if len(closed.Evidence) != 1 || closed.Evidence[0].Kind != plan.EvidenceExecute || closed.Evidence[0].ReferenceID != "exec-1" {
		t.Fatalf("expected execute evidence, got %+v", closed.Evidence)
	}

	// failure: exit 1 -> blocked with reason
	created2, err := rt.plans.Create(ctx, remoteID, principalID, plan.CreateInput{
		Goal: "g2", Tasks: []plan.TaskInput{{Title: "t2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID2 := created2.Tasks[0].ID
	if _, err := rt.ensurePlanTaskInProgress(ctx, remoteID, principalID, created2.ID, taskID2); err != nil {
		t.Fatal(err)
	}
	blocked, err := rt.closePlanTaskAfterPi(ctx, remoteID, principalID, created2.ID, taskID2, "exec-2", map[string]any{"exit_code": 1, "stderr": "boom"})
	if err != nil || blocked.Status != plan.TaskBlocked {
		t.Fatalf("close failure: task=%+v err=%v", blocked, err)
	}
	if !strings.Contains(blocked.Evidence[0].ReferenceID, "exec-2") || blocked.Evidence[0].Kind != plan.EvidenceExecute {
		t.Fatalf("expected execute evidence on block, got %+v", blocked.Evidence)
	}
}

func TestResolvePiLauncher(t *testing.T) {
	exe, args, err := resolvePiLauncher("do the thing", "", "context note")
	if err != nil {
		t.Skipf("pi agent not available: %v", err)
	}
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-p" && args[i+1] == "do the thing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("launcher args missing -p prompt: exe=%s args=%v", exe, args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--no-session") || !strings.Contains(joined, "--approve") || !strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("launcher args incomplete: %s", joined)
	}
	if exe == "" {
		t.Fatal("launcher executable is empty")
	}
}
