package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/approval"
	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/logging"
	"mcpx/internal/remotesession"
	"mcpx/internal/tasks"
)

// pi_window delivers tasks to already-running Pi agent windows through the
// pi-maestro-teammate workspace-peer file protocol (the same transport that
// powers teammate-send). Windows auto-publish owner snapshots under
// ~/.pi/teammate/workspaces/{workspaceId}/runtime/owners; commands are
// dropped into .../runtime/commands/{ownerId}/{commandId}.json and the target
// window injects them into its main session as teammate-messages.

const (
	piPeerProtocolVersion    = 1
	piPeerWindowMainSession  = "window-main-session"
	piPeerStaleMs            = 20_000
	piPeerCommandTTL         = 5 * time.Minute
	piPeerMaxCommandBytes    = 64 * 1024
	defaultPiWindowWaitMs    = 30_000
	maxPiWindowWaitMs        = 120_000
	piPeerCommandPollStepMs  = 500
	piPeerMessageKindRequest = "request"
	piPeerMessageSource      = "system"
)

var (
	piOwnerIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	piHex64Pattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	piControlChars   = "\r\n\t\u0085\u2028\u2029"
	// resolvePiPeerRuntimeRoot maps a workspace path to the workspace-peer
	// runtime root (injectable for tests).
	resolvePiPeerRuntimeRoot = defaultPiPeerRuntimeRoot
)

// defaultPiPeerRuntimeRoot mirrors the pi plugin algorithm:
// ~/.pi/teammate/workspaces/{sha256(normalizedCwd)}/runtime
func defaultPiPeerRuntimeRoot(workspacePath string) (string, error) {
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", err
	}
	normalized := strings.ReplaceAll(abs, "\\", "/")
	normalized = strings.TrimRight(normalized, "/")
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	digest := sha256.Sum256([]byte(normalized))
	workspaceID := hex.EncodeToString(digest[:])
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "teammate", "workspaces", workspaceID, "runtime"), nil
}

type piPeerIdentity struct {
	OwnerID    string `json:"owner_id"`
	OwnerNonce string `json:"owner_nonce"`
}

func randomPiHexID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// piPeerIdentityPath loads or creates the mcpx peer identity used as the
// command sender (fromOwnerId/fromOwnerNonce).
func piPeerIdentityPath() (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "pi-peer-identity.json"), nil
}

func loadPiPeerIdentity() (piPeerIdentity, error) {
	path, err := piPeerIdentityPath()
	if err != nil {
		return piPeerIdentity{}, err
	}
	if raw, err := os.ReadFile(path); err == nil {
		var identity piPeerIdentity
		if json.Unmarshal(raw, &identity) == nil &&
			piOwnerIDPattern.MatchString(identity.OwnerID) &&
			piOwnerIDPattern.MatchString(identity.OwnerNonce) {
			return identity, nil
		}
	}
	identity := piPeerIdentity{OwnerID: randomPiHexID(), OwnerNonce: randomPiHexID()}
	raw, _ := json.Marshal(identity)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return piPeerIdentity{}, err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return piPeerIdentity{}, err
	}
	return identity, nil
}

// piOwnerSnapshot is the subset of the workspace-peer owner snapshot the
// tool reads for discovery.
type piOwnerSnapshot struct {
	Version         int               `json:"version"`
	Kind            string            `json:"kind"`
	WorkspaceID     string            `json:"workspaceId"`
	NormalizedCwd   string            `json:"normalizedCwd"`
	OwnerID         string            `json:"ownerId"`
	OwnerNonce      string            `json:"ownerNonce"`
	PID             int               `json:"pid"`
	PublishedAt     int64             `json:"publishedAt"`
	SessionID       string            `json:"sessionId,omitempty"`
	SessionName     string            `json:"sessionName,omitempty"`
	ContextPressure *int              `json:"contextPressure,omitempty"`
	MainActivityAt  int64             `json:"mainActivityAt,omitempty"`
	Agents          []json.RawMessage `json:"agents"`
	Settled         []json.RawMessage `json:"settled"`
}

func (s *piOwnerSnapshot) valid() bool {
	return s != nil && s.Version == piPeerProtocolVersion && s.Kind == "owner" &&
		piHex64Pattern.MatchString(s.WorkspaceID) &&
		piOwnerIDPattern.MatchString(s.OwnerID) &&
		piOwnerIDPattern.MatchString(s.OwnerNonce)
}

// piPeerCommand mirrors WorkspacePeerCommand from the pi plugin.
type piPeerCommand struct {
	Version           int    `json:"version"`
	Kind              string `json:"kind"`
	WorkspaceID       string `json:"workspaceId"`
	CommandID         string `json:"commandId"`
	FromOwnerID       string `json:"fromOwnerId"`
	FromOwnerNonce    string `json:"fromOwnerNonce"`
	ToOwnerID         string `json:"toOwnerId"`
	ToOwnerNonce      string `json:"toOwnerNonce"`
	TargetCorrelation string `json:"targetCorrelationId"`
	Action            string `json:"action"`
	Message           string `json:"message"`
	Source            string `json:"source"`
	MessageKind       string `json:"messageKind"`
	ReplyTo           string `json:"replyTo"`
	FromSessionName   string `json:"fromSessionName"`
	CreatedAt         int64  `json:"createdAt"`
	ExpiresAt         int64  `json:"expiresAt"`
}

type piPeerCommandResponse struct {
	Version           int    `json:"version"`
	Kind              string `json:"kind"`
	WorkspaceID       string `json:"workspaceId"`
	CommandID         string `json:"commandId"`
	FromOwnerID       string `json:"fromOwnerId"`
	FromOwnerNonce    string `json:"fromOwnerNonce"`
	ToOwnerID         string `json:"toOwnerId"`
	ToOwnerNonce      string `json:"toOwnerNonce"`
	TargetCorrelation string `json:"targetCorrelationId"`
	Status            string `json:"status"`
	Message           string `json:"message,omitempty"`
	EffectiveAction   string `json:"effectiveAction,omitempty"`
	DeliveryStage     string `json:"deliveryStage,omitempty"`
	RespondedAt       int64  `json:"respondedAt"`
	ExpiresAt         int64  `json:"expiresAt"`
}

func (r *piPeerCommandResponse) valid() bool {
	return r != nil && r.Version == piPeerProtocolVersion && r.Kind == "response" &&
		r.CommandID != "" && r.Status != ""
}

// piWindowListing is the model-visible window entry.
type piWindowListing struct {
	DisplayName     string `json:"display_name"`
	SessionName     string `json:"session_name,omitempty"`
	Target          string `json:"target"`
	OwnerID         string `json:"owner_id"`
	PID             int    `json:"pid"`
	PublishedAt     int64  `json:"published_at"`
	AgentCount      int    `json:"agent_count"`
	ContextPressure *int   `json:"context_pressure,omitempty"`
}

func piWindowDisplayName(sessionName, ownerID string) string {
	if strings.TrimSpace(sessionName) != "" {
		return sessionName
	}
	return "window:" + ownerID[:8]
}

// listPiWindows discovers fresh owner snapshots for a workspace.
func listPiWindows(workspacePath string, now time.Time) ([]piOwnerSnapshot, []piWindowListing, error) {
	root, err := resolvePiPeerRuntimeRoot(workspacePath)
	if err != nil {
		return nil, nil, err
	}
	ownersDir := filepath.Join(root, "owners")
	entries, err := os.ReadDir(ownersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var snapshots []piOwnerSnapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(ownersDir, entry.Name()))
		if err != nil {
			continue
		}
		var snapshot piOwnerSnapshot
		if json.Unmarshal(raw, &snapshot) != nil || !snapshot.valid() {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	// Freshness: keep snapshots published within the peer stale window.
	fresh := snapshots[:0]
	for _, snapshot := range snapshots {
		if now.UnixMilli()-snapshot.PublishedAt <= piPeerStaleMs {
			fresh = append(fresh, snapshot)
		}
	}
	listings := make([]piWindowListing, 0, len(fresh))
	for _, snapshot := range fresh {
		listings = append(listings, piWindowListing{
			DisplayName:     piWindowDisplayName(snapshot.SessionName, snapshot.OwnerID),
			SessionName:     snapshot.SessionName,
			Target:          "owner:" + snapshot.OwnerID,
			OwnerID:         snapshot.OwnerID,
			PID:             snapshot.PID,
			PublishedAt:     snapshot.PublishedAt,
			AgentCount:      len(snapshot.Agents),
			ContextPressure: snapshot.ContextPressure,
		})
	}
	return fresh, listings, nil
}

// resolvePiWindow matches a selector against fresh windows: exact ownerId,
// display/session name, or a unique ownerId prefix.
func resolvePiWindow(snapshots []piOwnerSnapshot, selector string) (*piOwnerSnapshot, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, fmt.Errorf("window is required; use pi_window list first")
	}
	if piOwnerIDPattern.MatchString(selector) {
		for i := range snapshots {
			if snapshots[i].OwnerID == selector {
				return &snapshots[i], nil
			}
		}
		return nil, fmt.Errorf("window %q not found among fresh windows", selector)
	}
	var matches []*piOwnerSnapshot
	for i := range snapshots {
		snapshot := &snapshots[i]
		if snapshot.OwnerID == selector || strings.HasPrefix(snapshot.OwnerID, selector) ||
			piWindowDisplayName(snapshot.SessionName, snapshot.OwnerID) == selector {
			matches = append(matches, snapshot)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("window %q not found; use pi_window list to discover fresh windows", selector)
	default:
		return nil, fmt.Errorf("window %q is ambiguous; use the exact owner_id", selector)
	}
}

// deliverPiWindowCommand writes the command file and waits for the response.
// The caller supplies the command ID so wrappers (task_delegate) can bind it
// to their own identifiers before delivery.
func deliverPiWindowCommand(workspacePath string, target piOwnerSnapshot, identity piPeerIdentity, commandID, action, message string, wait time.Duration) (piPeerCommand, *piPeerCommandResponse, error) {
	root, err := resolvePiPeerRuntimeRoot(workspacePath)
	if err != nil {
		return piPeerCommand{}, nil, err
	}
	now := time.Now().UnixMilli()
	command := piPeerCommand{
		Version: piPeerProtocolVersion, Kind: "command",
		WorkspaceID: target.WorkspaceID, CommandID: commandID,
		FromOwnerID: identity.OwnerID, FromOwnerNonce: identity.OwnerNonce,
		ToOwnerID: target.OwnerID, ToOwnerNonce: target.OwnerNonce,
		TargetCorrelation: piPeerWindowMainSession,
		Action:            action, Message: message,
		Source: piPeerMessageSource, MessageKind: piPeerMessageKindRequest,
		ReplyTo: "owner:" + identity.OwnerID, FromSessionName: "mcpx",
		CreatedAt: now, ExpiresAt: now + piPeerCommandTTL.Milliseconds(),
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return piPeerCommand{}, nil, err
	}
	commandDir := filepath.Join(root, "commands", target.OwnerID)
	if err := os.MkdirAll(commandDir, 0o700); err != nil {
		return piPeerCommand{}, nil, err
	}
	targetPath := filepath.Join(commandDir, commandID+".json")
	if err := os.WriteFile(targetPath, raw, 0o600); err != nil {
		return piPeerCommand{}, nil, err
	}
	responseDir := filepath.Join(root, "responses", identity.OwnerID)
	responsePath := filepath.Join(responseDir, commandID+".json")
	response, err := waitForPiPeerResponse(responsePath, commandID, wait)
	return command, response, err
}

// waitForPiPeerResponse polls the response file for the given command.
func waitForPiPeerResponse(responsePath, commandID string, wait time.Duration) (*piPeerCommandResponse, error) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(responsePath); err == nil {
			var response piPeerCommandResponse
			if json.Unmarshal(raw, &response) == nil && response.valid() && response.CommandID == commandID {
				return &response, nil
			}
		}
		time.Sleep(piPeerCommandPollStepMs)
	}
	return nil, nil
}

func validatePiPeerMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("message is required")
	}
	if len(message) > piPeerMaxCommandBytes {
		return fmt.Errorf("message exceeds %d bytes", piPeerMaxCommandBytes)
	}
	if strings.ContainsAny(message, piControlChars) {
		return fmt.Errorf("message must not contain control characters")
	}
	return nil
}

// toolPiWindow lists and messages running Pi agent windows.
func (r *Runtime) toolPiWindow(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	envReq, principal, remote, fail := r.changeRequest(ctx, req, action != "list")
	if fail != nil {
		return fail, nil
	}
	now := time.Now()
	snapshots, listings, err := listPiWindows(remote.WorkspacePath, now)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "peer_discovery_error", err.Error())
	}
	switch action {
	case "list":
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{
			"workspace": remote.WorkspaceName, "windows": listings,
		})
	case "send":
		return r.piWindowSend(ctx, envReq, principal, remote, snapshots, listings, now)
	default:
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "INVALID_ACTION", fmt.Sprintf("pi_window does not support action %q", action))
	}
}

func (r *Runtime) piWindowSend(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session,
	snapshots []piOwnerSnapshot, listings []piWindowListing, now time.Time) (*mcp.CallToolResult, error) {
	return r.deliverPiWindow(ctx, envReq, principal, remote, snapshots, listings, now, piSendOptions{})
}

// piSendOptions parameterizes the shared confirmation + delivery flow used by
// pi_window send and task_delegate. The zero value reproduces plain pi_window
// send semantics.
type piSendOptions struct {
	// toolName labels approval entries, audit events and retry hints.
	// Defaults to "pi_window".
	toolName string
	// commandID pre-generates the peer command ID; when empty one is
	// generated at delivery time. task_delegate sets it so the delegated
	// task_id and the peer commandID are identical.
	commandID string
	// deliveryMessage overrides the payload message delivered to the window
	// (task_delegate appends its result-writeback instruction). Empty uses
	// the payload "message".
	deliveryMessage string
	// digestMessage is the message bound to the confirmation digest and shown
	// in confirmation/retry payloads; empty falls back to deliveryMessage.
	// task_delegate keeps the user-facing original message here so the
	// confirmation digest stays stable across the user_confirmed retry even
	// though the delivered message carries a fresh per-attempt task_id.
	digestMessage string
	// extraData is merged into the delivery success data.
	extraData map[string]any
	// confirmationNote is an additional note rendered in the confirmation
	// payload.
	confirmationNote string
}

// deliverPiWindow is the shared send flow: window resolution, confirmation
// gate, peer delivery, approval consumption and delegated-task registration.
func (r *Runtime) deliverPiWindow(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session,
	snapshots []piOwnerSnapshot, listings []piWindowListing, now time.Time, opts piSendOptions) (*mcp.CallToolResult, error) {
	toolName := opts.toolName
	if toolName == "" {
		toolName = "pi_window"
	}
	purpose := strings.TrimSpace(envReq.Intent)
	selector := strings.TrimSpace(stringPayload(envReq.Payload, "window"))
	message := opts.deliveryMessage
	if message == "" {
		message = stringPayload(envReq.Payload, "message")
	}
	digestMessage := opts.digestMessage
	if digestMessage == "" {
		digestMessage = message
	}
	action := strings.TrimSpace(stringPayload(envReq.Payload, "mode"))
	if action == "" {
		// Default to steer: inject into the target window's current turn so the
		// task is handled immediately instead of queueing behind its work.
		action = "steer"
	}
	if action == "" {
		action = "follow_up"
	}
	if action != "steer" && action != "follow_up" {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "action must be steer or follow_up")
	}
	target, err := resolvePiWindow(snapshots, selector)
	if err != nil {
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName,
			map[string]any{"windows": listings, "retry_hint": "call pi_window list to discover fresh windows, then retry with an exact window or owner_id"},
			"WINDOW_NOT_FOUND", err.Error())
		response.RemoteSessionID = remote.ID
		addRecoveryAction(&response, "pi_window", "先用 pi_window list 查看当前可发现的 Pi 窗口，再用返回的 display_name 或 owner_id 重试", map[string]any{
			"remote_session_id": remote.ID, "action": "list",
		})
		return r.resultJSON(response)
	}
	if err := validatePiPeerMessage(message); err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", err.Error())
	}
	planID := strings.TrimSpace(stringPayload(envReq.Payload, "plan_id"))
	planTaskID := strings.TrimSpace(stringPayload(envReq.Payload, "plan_task_id"))
	if (planID == "") != (planTaskID == "") {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "bad_request", "plan_id and plan_task_id must be provided together")
	}
	waitMs := intPayload(envReq.Payload, "wait_time_ms")
	if waitMs <= 0 {
		waitMs = defaultPiWindowWaitMs
	}
	if waitMs > maxPiWindowWaitMs {
		waitMs = maxPiWindowWaitMs
	}

	// Confirmation gate: dispatch to a user's window is always user-confirmed.
	// The digest binds the user-facing message (task_delegate's per-attempt
	// task_id lives only in the delivered message).
	digest := piWindowDigest(remote.ID, remote.WorkspaceName, target.OwnerID, action, digestMessage, purpose)
	userConfirmed := boolPayload(envReq.Payload, "user_confirmed")
	pending, pendingOK := r.piWindowPendingConfirmation(toolName, remote.ID, principal.ID, digest)
	if !userConfirmed || !pendingOK {
		if !pendingOK {
			contentKey := cleanCommandConfirmationContentKey(principal.ID, digest)
			if toolName != "pi_window" {
				// Scope the dedup key so a same-message pi_window pending is
				// never folded onto a task_delegate confirmation (or back).
				contentKey += "\x00" + toolName
			}
			var confirmationErr error
			pending, confirmationErr = r.approvals.PutPending(approval.Pending{
				Tool: toolName, Summary: fmt.Sprintf("dispatch to window %s: %s", target.OwnerID[:8], truncateRunes(digestMessage, 120)),
				Command: digest, CommandDigest: digest, Purpose: purpose, Scope: "workspace",
				WorkDir: remote.WorkspacePath, RequestID: envReq.RequestID, Workspace: remote.WorkspaceName,
				RemoteSessionID: remote.ID, PrincipalID: principal.ID,
				ContentKey: contentKey,
			})
			if confirmationErr != nil {
				return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "confirmation_store_error", confirmationErr.Error())
			}
		}
		confirmationData := map[string]any{
			"window": piWindowDisplayName(target.SessionName, target.OwnerID), "owner_id": target.OwnerID,
			"action": action, "message": truncateRunes(digestMessage, 400),
			"pending_digest": digest, "confirmation_required": true, "user_confirmed_required": true,
			"summary": "将任务委派到 Pi 窗口前需要用户确认；请向用户展示目标窗口与消息摘要，确认后将 user_confirmed=true 原样重试。",
		}
		if opts.confirmationNote != "" {
			confirmationData["note"] = opts.confirmationNote
		}
		response := envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, remote.WorkspaceName,
			confirmationData, "USER_CONFIRMATION_REQUIRED", "Pi 窗口任务派发等待用户语义确认")
		response.RemoteSessionID = remote.ID
		addRecoveryAction(&response, toolName, "用户确认后使用相同 window、message、purpose 和 remote_session_id 重试，并设置 user_confirmed=true", map[string]any{
			"remote_session_id": remote.ID, "action": "send", "window": selector,
			"message": digestMessage, "purpose": purpose, "user_confirmed": true,
			"plan_id": planID, "plan_task_id": planTaskID, "wait_time_ms": waitMs,
		})
		return r.resultJSON(response)
	}

	identity, err := loadPiPeerIdentity()
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "peer_identity_error", err.Error())
	}
	commandID := opts.commandID
	if commandID == "" {
		commandID = randomPiHexID()
	}
	command, response, err := deliverPiWindowCommand(remote.WorkspacePath, *target, identity, commandID, action, message, time.Duration(waitMs)*time.Millisecond)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "peer_delivery_error", err.Error())
	}
	if _, consumed := r.approvals.Consume(pending.ID); !consumed {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "confirmation_state_error", "confirmed pi_window approval could not be consumed")
	}

	// Delivery succeeded (the command file carries the durable commandID):
	// record the delegation so task_result_view can track it. A registry
	// write failure must not fail an already-delivered dispatch.
	deliveredAt := time.Now().UTC()
	if err := r.delegated.Put(tasks.DelegatedTask{
		TaskID:          command.CommandID,
		RemoteSessionID: remote.ID,
		Workspace:       remote.WorkspaceName,
		TargetOwnerID:   target.OwnerID,
		SpawnPID:        target.PID,
		Action:          action,
		Message:         message,
		Purpose:         purpose,
		Status:          tasks.StatusDelivered,
		CreatedAt:       deliveredAt,
		DeliveredAt:     &deliveredAt,
	}); err != nil {
		logging.With("component", "delegated_tasks").Error("register delegated task failed",
			"task_id", command.CommandID, "remote_session_id", remote.ID, "err", err)
	}

	data := map[string]any{
		"command_id": command.CommandID, "task_id": command.CommandID,
		"window":   piWindowDisplayName(target.SessionName, target.OwnerID),
		"owner_id": target.OwnerID, "action": command.Action, "message_kind": command.MessageKind,
		"expires_at": command.ExpiresAt,
	}
	for key, value := range opts.extraData {
		data[key] = value
	}
	if response != nil {
		data["delivery"] = response.Status
		if response.EffectiveAction != "" {
			data["effective_action"] = response.EffectiveAction
		}
		if response.DeliveryStage != "" {
			data["delivery_stage"] = response.DeliveryStage
		}
		if response.Message != "" {
			data["delivery_message"] = response.Message
		}
		data["delivered"] = response.Status == "accepted"
		switch response.Status {
		case "accepted":
			if planTaskID != "" {
				if _, err := r.ensurePlanTaskInProgress(ctx, remote.ID, principal.ID, planID, planTaskID); err != nil {
					return r.planError(envReq, remote, err)
				}
				item, err := r.plans.Get(ctx, remote.ID, planID)
				if err == nil {
					for i := range item.Tasks {
						if item.Tasks[i].ID == planTaskID {
							data["plan_task"] = planTaskMap(planID, item.Tasks[i])
							break
						}
					}
				}
			}
			r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: toolName, Status: "ok", Detail: map[string]any{
				"command_id": command.CommandID, "owner_id": target.OwnerID, "action": command.Action, "plan_task_id": planTaskID,
			}})
			return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, data)
		case "rejected", "expired", "error":
			return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "PEER_DELIVERY_"+strings.ToUpper(response.Status), fmt.Sprintf("窗口投递%s：%s", response.Status, response.Message))
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, data)
	}
	data["delivered"] = false
	data["delivery"] = "pending"
	data["next_action"] = nextAction(toolName, map[string]any{
		"remote_session_id": remote.ID, "action": "send", "window": target.OwnerID,
		"message": digestMessage, "user_confirmed": true, "wait_time_ms": waitMs,
	})
	return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, data)
}

func piWindowDigest(remoteSessionID, workspace, ownerID, action, message, purpose string) string {
	// Request IDs identify transport attempts; they must not change the
	// semantic digest used to bind confirmation across retry.
	value := strings.Join([]string{remoteSessionID, workspace, ownerID, action, message, purpose}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// piWindowPendingConfirmation finds the pending approval recorded under
// toolName for the exact digest (the shared pendingCommandConfirmation helper
// is bound to the command_execute tool).
func (r *Runtime) piWindowPendingConfirmation(toolName, remoteSessionID, principalID, digest string) (approval.Pending, bool) {
	for _, pending := range r.approvals.ListRemoteSession(remoteSessionID) {
		if pending.Tool == toolName && pending.PrincipalID == principalID &&
			pending.Command == digest && pending.CommandDigest == digest {
			return pending, true
		}
	}
	return approval.Pending{}, false
}
