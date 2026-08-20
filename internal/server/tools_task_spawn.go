package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/config"
	"mcpx/internal/logging"
	"mcpx/internal/remotesession"
	"mcpx/internal/tasks"
)

// taskSpawnInjectionFormat appends the result-writeback + exit instruction to
// the message handed to the spawned headless pi process. Unlike the peer
// transport (task_delegate), the spawned pi process receives the message as a
// CLI argv, so control-character restrictions do not apply; the format stays
// single-line and mirrors the delegated writeback schema so task_result_view
// can merge the result file once pi writes it and exits.
const taskSpawnInjectionFormat = taskDelegateInjectionFormat + " 写入结果文件后请直接退出。"

// taskSpawnInjection builds the injected instruction carrying the absolute
// result path, task id and exit directive appended to the user message.
func taskSpawnInjection(taskID, resultPath string) string {
	return fmt.Sprintf(taskSpawnInjectionFormat, resultPath, taskID)
}

// spawnDetachedConfig is defined in spawn_detached_windows.go / spawn_detached_unix.go
// (build-tag split) because syscall.SysProcAttr fields are platform-specific.

// piSpawnCommandBuilder returns the *exec.Cmd for the spawned headless pi
// process. It is a package-level variable so tests can substitute a fake
// executable without launching the real pi agent. The default resolves the
// pi launcher through the PATH.
var piSpawnCommandBuilder = func(prompt string) *exec.Cmd {
	return exec.Command("pi", "-p", prompt)
}

// toolTaskSpawn starts a detached headless pi process (pi -p <message+injection>)
// in the resolved workspace, redirects its stdout/stderr to a per-task log
// file, records the task in the delegated registry as executing, and returns
// immediately without waiting for the process to finish. The spawned pi agent
// writes its result to the instructed result path and exits; task_result_view
// merges that file once it lands.
func (r *Runtime) toolTaskSpawn(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	if r.delegated == nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "registry_unavailable", "delegated task registry is not initialized")
	}
	message := stringPayload(envReq.Payload, "message")
	if err := validateSpawnMessage(message); err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", err.Error())
	}
	// The optional workspace argument rides on the envelope's Workspace field
	// (the wire-level "workspace" key is parsed there, not into Payload).
	workspaceName := strings.TrimSpace(envReq.Workspace)
	workspacePath := remote.WorkspacePath
	if workspaceName != "" {
		ws, ok := r.reg.Load().Get(workspaceName)
		if !ok {
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "workspace_not_found", fmt.Sprintf("workspace %q not found", workspaceName))
		}
		workspacePath = ws.Path
	}

	now := time.Now().UTC()
	taskID := randomPiHexID()
	resultPath := r.delegated.ResultPath(remote.ID, taskID)
	injectedMessage := message + taskSpawnInjection(taskID, resultPath)

	home, err := config.HomeDir()
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "home_unavailable", err.Error())
	}
	logDir := filepath.Join(home, "tasks", "delegated", remote.ID)
	if mkErr := os.MkdirAll(logDir, 0o700); mkErr != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "log_dir_unavailable", mkErr.Error())
	}
	logPath := filepath.Join(logDir, taskID+".log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "log_open_error", err.Error())
	}

	cmd := piSpawnCommandBuilder(injectedMessage)
	cmd.Dir = workspacePath
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = spawnDetachedConfig()

	if startErr := cmd.Start(); startErr != nil {
		_ = logFile.Close()
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "spawn_error", startErr.Error())
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	// The child holds its own copy of the log fd once started; closing the
	// parent handle does not affect the detached process's stream.
	_ = logFile.Close()
	// Release the child's process record so MCPX does not wait on it or keep
	// a zombie entry.
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}

	if putErr := r.delegated.Put(tasks.DelegatedTask{
		TaskID:          taskID,
		RemoteSessionID: remote.ID,
		Workspace:       remote.WorkspaceName,
		Action:          "spawn",
		SpawnPID:        pid,
		Message:         message,
		Purpose:         strings.TrimSpace(envReq.Intent),
		Status:          tasks.StatusExecuting,
		CreatedAt:       now,
	}); putErr != nil {
		logging.With("component", "delegated_tasks").Error("register spawned task failed",
			"task_id", taskID, "remote_session_id", remote.ID, "err", putErr)
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "registry_write_error", putErr.Error())
	}

	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{
		RemoteSessionID: remote.ID, Type: "task_spawn.started", OperationID: taskID,
		Summary: truncateRunes(message, 200),
		Metadata: map[string]any{
			"task_id": taskID, "pid": pid, "workspace": remote.WorkspaceName,
			"log_path": logPath, "result_path": resultPath,
		},
	})

	data := map[string]any{
		"task_id":     taskID,
		"result_path": resultPath,
		"pid":         pid,
		"log_path":    logPath,
		"workspace":   remote.WorkspaceName,
		"status":      tasks.StatusExecuting,
		"next_action": nextAction("task_result_view", map[string]any{
			"remote_session_id": remote.ID,
			"delegated_task_id": taskID,
		}),
		"summary": fmt.Sprintf("Detached pi process %d spawned in workspace %q; result will be written to %s.", pid, remote.WorkspaceName, resultPath),
	}
	r.logAudit(audit.Event{
		RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName,
		Tool: "task_spawn", Status: "spawned", Detail: map[string]any{
			"task_id": taskID, "pid": pid, "log_path": logPath, "result_path": resultPath,
		},
	})
	return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, data)
}

// validateSpawnMessage mirrors the peer-message contract for the spawned
// process: a non-empty message is required. Control characters are tolerated
// by the local argv but rejected here to keep the two delegation paths
// consistent and to avoid confusing the pi agent.
func validateSpawnMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("message is required")
	}
	if len(message) > piPeerMaxCommandBytes {
		return fmt.Errorf("message exceeds %d bytes", piPeerMaxCommandBytes)
	}
	return nil
}
