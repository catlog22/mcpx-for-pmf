package envelope

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const MaxIntentBytes = 512

// Status is the unified response status.
type Status string

const (
	StatusOK               Status = "ok"
	StatusNeedConfirmation Status = "need_confirmation"
	StatusNeedSecret       Status = "need_secret"
	StatusDenied           Status = "denied"
	StatusUnauthorized     Status = "unauthorized"
	StatusError            Status = "error"
)

// Request is the common tool arguments envelope.
type Request struct {
	// RequestID and StartedAtMs are populated by the Gateway Runtime Context,
	// never by MCP tool arguments.
	RequestID       string         `json:"-"`
	Intent          string         `json:"intent,omitempty"`
	RemoteSessionID string         `json:"remote_session_id,omitempty"`
	Workspace       string         `json:"workspace,omitempty"`
	StartedAtMs     int64          `json:"-"`
	Payload         map[string]any `json:"payload"`
}

// ErrorBody is the machine-readable error contract returned by every failed
// tool call. Clients must branch on category/code rather than parsing Message.
type ErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Category  string         `json:"category"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// Response is the internal handler response assembled before the server's
// public ARC instrumentation boundary.
type Response struct {
	OK               bool       `json:"ok"`
	Status           Status     `json:"status"`
	RequestID        string     `json:"request_id"`
	RemoteSessionID  string     `json:"remote_session_id,omitempty"`
	Workspace        string     `json:"workspace,omitempty"`
	StartedAtMs      int64      `json:"started_at_ms"`
	ReceivedAtMs     int64      `json:"received_at_ms"`
	CompletedAtMs    int64      `json:"completed_at_ms"`
	NetworkLatencyMs int64      `json:"network_latency_ms"`
	ProcessingMs     int64      `json:"processing_ms"`
	ServerElapsedMs  int64      `json:"server_elapsed_ms"`
	Data             any        `json:"data"`
	Error            *ErrorBody `json:"error"`
}

// OK builds a successful response.
func OK(requestID, workspace string, data any) Response {
	return Response{
		OK:        true,
		Status:    StatusOK,
		RequestID: requestID,
		Workspace: workspace,
		Data:      data,
		Error:     nil,
	}
}

// Fail builds an internal non-OK response with a normalized error body.
// The public MCP boundary converts this payload to an ARC error result.
func Fail(status Status, requestID, workspace string, data any, code, msg string) Response {
	if code == "" {
		switch status {
		case StatusNeedConfirmation:
			code = "APPROVAL_REQUIRED"
		case StatusNeedSecret:
			code = "SECRET_REQUIRED"
		case StatusUnauthorized:
			code = "UNAUTHORIZED"
		case StatusDenied:
			code = "DENIED"
		default:
			code = "INTERNAL_ERROR"
		}
	}
	if msg == "" {
		msg = strings.ToLower(strings.ReplaceAll(code, "_", " "))
	}
	code = strings.ToUpper(code)
	category, retryable, hint := classifyError(status, code)
	details := map[string]any{}
	if hint != "" {
		details["retry_hint"] = hint
	}
	return Response{
		OK:        false,
		Status:    status,
		RequestID: requestID,
		Workspace: workspace,
		Data:      data,
		Error: &ErrorBody{
			Code: code, Message: msg, Category: category, Retryable: retryable, Details: details,
		},
	}
}

func classifyError(status Status, code string) (category string, retryable bool, retryHint string) {
	if status == StatusUnauthorized || strings.Contains(code, "FORBIDDEN") || strings.Contains(code, "DENIED") || strings.Contains(code, "APPROVAL") || strings.Contains(code, "SECRET") {
		return "permission", false, "Request the required permission or provide the required approval."
	}
	if strings.Contains(code, "NOT_FOUND") || strings.Contains(code, "WORKSPACE_NOT_FOUND") {
		return "not_found", false, "Check the identifier and refresh the relevant list."
	}
	if strings.Contains(code, "STALE") || strings.Contains(code, "CONFLICT") || strings.Contains(code, "VERSION") || strings.Contains(code, "PATCH_CONTEXT") || strings.Contains(code, "PATCH_HUNKS") || strings.Contains(code, "ROLLBACK") {
		return "conflict", true, "Read the current revision and regenerate the operation."
	}
	if strings.Contains(code, "INVALID") || strings.Contains(code, "BAD_REQUEST") || strings.Contains(code, "VALIDATION") || strings.Contains(code, "UNSUPPORTED") || strings.Contains(code, "REQUIRED") || strings.Contains(code, "AMBIGUOUS") {
		return "validation", false, "Correct the request arguments and retry."
	}
	if strings.Contains(code, "START") || strings.Contains(code, "EXEC") || strings.Contains(code, "RUNTIME") || strings.Contains(code, "TIMEOUT") || strings.Contains(code, "INTERRUPTED") {
		return "runtime", true, "Retry when the runtime is available or inspect the Task logs."
	}
	return "internal", false, "Inspect the server log and retry with a new request id if appropriate."
}

// EnsureRequestID returns id if non-empty, otherwise generates one.
func EnsureRequestID(id string) string {
	if id != "" {
		return id
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("req_%s_%s", time.Now().UTC().Format("20060102"), hex.EncodeToString(b[:]))
}

// ParseRequest decodes business tool arguments into Request.
// Runtime metadata is intentionally ignored and is injected by the Gateway.
func ParseRequest(raw json.RawMessage) (Request, error) {
	var req Request
	if len(raw) == 0 || string(raw) == "null" {
		req.Payload = map[string]any{}
		req.RequestID = EnsureRequestID("")
		return req, nil
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return Request{}, err
	}
	if req.Payload == nil {
		req.Payload = map[string]any{}
	}
	// Runtime metadata is deliberately discarded from the business payload.
	// The server repopulates it from Gateway Runtime Context after parsing.
	for key := range req.Payload {
		if isRuntimeField(key) {
			delete(req.Payload, key)
		}
	}
	// MCP tool schemas expose flat arguments. Normalize them into Payload so
	// handlers share one request representation; explicit Payload values win.
	var flat map[string]any
	if err := json.Unmarshal(raw, &flat); err != nil {
		return Request{}, err
	}
	for key, value := range flat {
		if key == "intent" || key == "remote_session_id" || key == "workspace" || key == "payload" || isRuntimeField(key) {
			continue
		}
		if _, exists := req.Payload[key]; !exists {
			req.Payload[key] = value
		}
	}
	req.RequestID = EnsureRequestID("")
	req.StartedAtMs = 0
	return req, nil
}

func isRuntimeField(key string) bool {
	switch key {
	case "request_id", "trace_id", "span_id", "started_at_ms", "received_at_ms", "completed_at_ms", "network_latency_ms", "processing_ms", "server_elapsed_ms", "client_info":
		return true
	default:
		return false
	}
}

// Marshal serializes a Response to JSON bytes.
func Marshal(resp Response) ([]byte, error) {
	return json.Marshal(resp)
}
