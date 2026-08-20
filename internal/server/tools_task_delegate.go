package server

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// taskDelegateInjectionFormat appends the result-writeback instruction to the
// delegated message. The peer transport rejects control characters, so the
// instruction must stay on a single line. The instructed JSON shape mirrors
// tasks.TaskResult so task_result_view can merge the file once the Pi agent
// writes it (status/result/result_summary/completed_at).
const taskDelegateInjectionFormat = " [系统·任务结果回传] 完成上述任务后，请用 write 工具将结果写入：%s 文件内容为 JSON：{\"task_id\":\"%s\",\"status\":\"completed\" 或 \"failed\",\"result\":\"任务结果文本\",\"result_summary\":[\"要点1\",\"要点2\"],\"completed_at\":\"ISO8601 时间\"}"

func taskDelegateInjection(taskID, resultPath string) string {
	return fmt.Sprintf(taskDelegateInjectionFormat, resultPath, taskID)
}

// toolTaskDelegate delegates a task to a running Pi window and instructs the
// agent to write the outcome back to the delegated-task result file. It is a
// thin wrapper over the pi_window send flow: it pre-generates the task ID
// (which doubles as the peer commandID), appends a result-writeback
// instruction to the message, and reuses the shared confirmation gate and
// delivery path. The returned task_id/result_path let callers poll
// task_result_view until the {taskID}.result.json file lands.
func (r *Runtime) toolTaskDelegate(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	if r.delegated == nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "registry_unavailable", "delegated task registry is not initialized")
	}
	message := stringPayload(envReq.Payload, "message")
	if err := validatePiPeerMessage(message); err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", err.Error())
	}
	now := time.Now()
	snapshots, listings, err := listPiWindows(remote.WorkspacePath, now)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "peer_discovery_error", err.Error())
	}
	taskID := randomPiHexID()
	resultPath := r.delegated.ResultPath(remote.ID, taskID)
	opts := piSendOptions{
		toolName:  "task_delegate",
		commandID: taskID,
		// The delivered message carries the writeback instruction; the
		// confirmation digest and previews bind the original message so the
		// user_confirmed retry matches the pending approval.
		deliveryMessage:  message + taskDelegateInjection(taskID, resultPath),
		digestMessage:    message,
		extraData:        map[string]any{"task_id": taskID, "result_path": resultPath},
		confirmationNote: "message 末尾将自动追加结果回传指令（用 write 工具写入结果文件）；确认投递后返回 task_id 与 result_path，结果用 task_result_view 查询。",
	}
	return r.deliverPiWindow(ctx, envReq, principal, remote, snapshots, listings, now, opts)
}
