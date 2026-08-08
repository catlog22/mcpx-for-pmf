package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/idempotency"
	"mcpx/internal/mcpresult"
	"mcpx/internal/remotesession"
)

// cleanIdempotencyFingerprint deliberately stores only a digest. The request
// payload can contain upstream arguments or one-shot values, so raw business
// parameters must never be persisted in the idempotency table.
func cleanIdempotencyFingerprint(operation string, payload map[string]any) string {
	canonical := make(map[string]any, len(payload)+1)
	canonical["operation"] = operation
	for key, value := range payload {
		switch key {
		case "idempotency_key", "user_confirmed", "confirmation_token", "client_request_id",
			"request_id", "purpose", "intent", "progress_summary", "execution_mode", "discovery_id", "discovery_revision":
			// These fields are retry/auth/audit metadata, not the effect itself.
		default:
			canonical[key] = value
		}
	}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type storedCleanToolResult struct {
	Structured json.RawMessage `json:"structured"`
	Text       string          `json:"text,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

func encodeCleanToolResult(result *mcp.CallToolResult) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("tool result is nil")
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, err
	}
	return json.Marshal(storedCleanToolResult{
		Structured: structured,
		Text:       mcpresult.FirstText(result),
		IsError:    result.IsError,
	})
}

func decodeCleanToolResult(encoded []byte, replay bool) (*mcp.CallToolResult, error) {
	var stored storedCleanToolResult
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return nil, err
	}
	if len(stored.Structured) == 0 || string(stored.Structured) == "null" {
		return nil, fmt.Errorf("stored tool result has no structured content")
	}
	var structured any
	if err := json.Unmarshal(stored.Structured, &structured); err != nil {
		return nil, err
	}
	if replay {
		markCleanReplay(structured)
	}
	result := mcpresult.NewStructured(structured, stored.Text)
	result.IsError = stored.IsError
	return result, nil
}

func markCleanReplay(value any) {
	wire, ok := value.(map[string]any)
	if !ok {
		return
	}
	if data, ok := wire["data"].(map[string]any); ok {
		data["idempotent_replay"] = true
		return
	}
	wire["idempotent_replay"] = true
}

func cleanResultState(result *mcp.CallToolResult) string {
	if result == nil {
		return idempotency.StateInDoubt
	}
	if wire, ok := result.StructuredContent.(map[string]any); ok {
		if status, _ := wire["status"].(string); status == string(envelope.StatusError) {
			return idempotency.StateFailed
		}
	}
	return idempotency.StateSucceeded
}

// withCleanIdempotency surrounds a mutating clean-core handler. The wrapped
// handler is deliberately called only after Claim has made this request the
// durable owner, so local concurrency and process-restart retries cannot
// launch a second effect.
func (r *Runtime) withCleanIdempotency(
	ctx context.Context,
	req *mcp.CallToolRequest,
	operation string,
	payload map[string]any,
	handler func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error),
	preflight ...func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error),
) (*mcp.CallToolResult, error) {
	keyValue := strings.TrimSpace(stringPayload(payload, "idempotency_key"))
	if keyValue == "" || r.idempotency == nil {
		return handler(ctx, req)
	}
	// Discovery is an explicit, model-visible prerequisite. Run its pure
	// validation before Claim so a missing or stale lease is not persisted as a
	// terminal replay that would poison the same business key after discover.
	if len(preflight) > 0 && preflight[0] != nil {
		result, preflightErr := preflight[0](ctx, req)
		if result != nil || preflightErr != nil {
			return result, preflightErr
		}
	}
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	key := idempotency.Key{
		RemoteSessionID: session.ID,
		PrincipalID:     principal.ID,
		Operation:       operation,
		Value:           keyValue,
	}
	fingerprint := cleanIdempotencyFingerprint(operation, payload)
	claim, err := r.idempotency.Claim(ctx, key, fingerprint, cleanIdempotencyTTL(operation))
	if err != nil {
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_STORE_ERROR", err.Error()), nil
	}
	switch claim.Kind {
	case idempotency.ClaimConflict:
		return r.cleanIdempotencyConflict(envReq, session, operation, payload, fingerprint, claim.Record.Fingerprint)
	case idempotency.ClaimReplay:
		result, decodeErr := decodeCleanToolResult(claim.Record.Response, true)
		if decodeErr != nil {
			return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_DOUBT", decodeErr.Error()), nil
		}
		return result, nil
	case idempotency.ClaimInDoubt:
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_DOUBT", "the previous request may have partially reached its target; reconcile before retrying"), nil
	case idempotency.ClaimWait:
		record, waitErr := r.idempotency.Wait(ctx, claim, key)
		if waitErr != nil {
			return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_PROGRESS", waitErr.Error()), nil
		}
		result, decodeErr := decodeCleanToolResult(record.Response, true)
		if decodeErr != nil {
			return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_DOUBT", decodeErr.Error()), nil
		}
		return result, nil
	case idempotency.ClaimPending:
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_PROGRESS", "the same idempotency request is still running"), nil
	case idempotency.ClaimOwner:
		// Continue below. The request is now the sole durable owner.
	default:
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_STORE_ERROR", "unknown idempotency claim state"), nil
	}

	result, callErr := handler(ctx, req)
	if result == nil {
		_ = r.idempotency.MarkInDoubt(ctx, key, fingerprint, nil)
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_DOUBT", "handler returned no durable result; reconcile before retrying"), callErr
	}
	encoded, encodeErr := encodeCleanToolResult(result)
	if encodeErr != nil {
		_ = r.idempotency.MarkInDoubt(ctx, key, fingerprint, nil)
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_DOUBT", encodeErr.Error()), callErr
	}
	state := cleanResultState(result)
	if callErr != nil && state == idempotency.StateSucceeded {
		state = idempotency.StateFailed
	}
	if completeErr := r.idempotency.Complete(ctx, key, fingerprint, state, encoded, []byte(`{"operation":"clean-core"}`)); completeErr != nil {
		_ = r.idempotency.MarkInDoubt(ctx, key, fingerprint, []byte(`{"recovery":"reconcile the target before retrying"}`))
		return r.cleanIdempotencyFailure(envReq, session, operation, payload, "IDEMPOTENCY_IN_DOUBT", completeErr.Error()), callErr
	}
	return result, callErr
}

func cleanIdempotencyTTL(operation string) time.Duration {
	if operation == "execute" {
		return 24 * time.Hour
	}
	return 24 * time.Hour
}

func (r *Runtime) cleanIdempotencyConflict(envReq envelope.Request, session remotesession.Session, operation string, payload map[string]any, current, original string) (*mcp.CallToolResult, error) {
	return r.cleanIdempotencyFailureWithDetails(envReq, session, operation, payload, "IDEMPOTENCY_CONFLICT", "idempotency_key is already bound to different business parameters", map[string]any{
		"current_fingerprint":  current,
		"original_fingerprint": original,
	})
}

func (r *Runtime) cleanIdempotencyFailure(envReq envelope.Request, session remotesession.Session, operation string, payload map[string]any, code, message string) *mcp.CallToolResult {
	result, _ := r.cleanIdempotencyFailureWithDetails(envReq, session, operation, payload, code, message, nil)
	return result
}

func (r *Runtime) cleanIdempotencyFailureWithDetails(envReq envelope.Request, session remotesession.Session, operation string, payload map[string]any, code, message string, details map[string]any) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, code, message)
	response.RemoteSessionID = session.ID
	if response.Error != nil {
		for key, value := range details {
			response.Error.Details[key] = value
		}
		response.Error.Details["idempotency_key_scope"] = "remote_session_id + principal_id + operation + idempotency_key"
		arguments := map[string]any{"remote_session_id": session.ID, "note": "使用新的 idempotency_key；不要重放未知状态的请求"}
		for _, key := range []string{"action", "plan_task_id", "execution_task_id", "plan_id", "artifact_id", "name", "server", "tool"} {
			if value, ok := payload[key]; ok {
				arguments[key] = value
			}
		}
		recoveryTool, recoveryAction := cleanIdempotencyRecovery(operation, payload)
		response.Error.Details["next_action"] = map[string]any{"tool": recoveryTool, "action": recoveryAction, "arguments": arguments}
		addRecoveryAction(&response, recoveryTool, "根据幂等状态恢复后再继续；变更业务参数时使用新的 idempotency_key", arguments)
	}
	return r.resultJSON(response)
}

func cleanIdempotencyRecovery(operation string, payload map[string]any) (tool, action string) {
	switch operation {
	case "execute":
		return "observe", "status"
	case "plan":
		return "plan", "read"
	case "artifact":
		if stringPayload(payload, "artifact_id") != "" {
			return "artifact", "read"
		}
		return "artifact", "list"
	case "skill_call", "mcp_call":
		return "discover", "describe"
	default:
		return operation, "read"
	}
}
