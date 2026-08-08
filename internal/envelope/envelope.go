package envelope

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	MaxIntentBytes        = 512
	MaxResultSummaryBytes = 8192
)

// Status is the unified response status.
type Status string

const (
	StatusOK               Status = "succeeded"
	StatusAccepted         Status = "accepted"
	StatusNeedConfirmation Status = "waiting_confirmation"
	StatusInterrupted      Status = "interrupted"
	StatusNeedSecret       Status = "failed"
	StatusDenied           Status = "failed"
	StatusUnauthorized     Status = "failed"
	StatusError            Status = "failed"
)

// Request is the common tool arguments envelope.
type Request struct {
	// RequestID and StartedAtMs are populated by the Gateway Runtime Context,
	// never by MCP tool arguments.
	RequestID         string         `json:"-"`
	OperationID       string         `json:"-"`
	ParentOperationID string         `json:"-"`
	StepID            string         `json:"-"`
	Purpose           string         `json:"purpose,omitempty"`
	Intent            string         `json:"intent,omitempty"`
	ProgressSummary   string         `json:"progress_summary,omitempty"`
	RemoteSessionID   string         `json:"remote_session_id,omitempty"`
	Workspace         string         `json:"workspace,omitempty"`
	StartedAtMs       int64          `json:"-"`
	Payload           map[string]any `json:"payload"`
}

// ErrorBody is the machine-readable error contract returned by every failed
// tool call. Clients must branch on category/code rather than parsing Message.
type ErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Category  string         `json:"category"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
	Recovery  *Recovery      `json:"recovery,omitempty"`
}

// Recovery describes the next public operation that can resolve an error.
// Arguments are suggestions only; authorization and policy are rechecked.
type Recovery struct {
	Action    string         `json:"action"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Response is the internal handler response assembled before the server's
// public ARC instrumentation boundary.
type Response struct {
	// OK is retained as an internal convenience for handlers and tests. It is
	// deliberately not serialized: Status is the only public outcome field.
	OK               bool       `json:"-"`
	Status           Status     `json:"-"`
	RequestID        string     `json:"-"`
	OperationID      string     `json:"-"`
	RemoteSessionID  string     `json:"-"`
	Workspace        string     `json:"-"`
	StartedAtMs      int64      `json:"-"`
	ReceivedAtMs     int64      `json:"-"`
	CompletedAtMs    int64      `json:"-"`
	NetworkLatencyMs int64      `json:"-"`
	ProcessingMs     int64      `json:"-"`
	ServerElapsedMs  int64      `json:"-"`
	Data             any        `json:"-"`
	Error            *ErrorBody `json:"-"`
}

type responseMeta struct {
	RequestID       string `json:"request_id,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	StartedAtMs     int64  `json:"started_at_ms,omitempty"`
	CompletedAtMs   int64  `json:"completed_at_ms,omitempty"`
	ProcessingMs    int64  `json:"processing_ms,omitempty"`
	ServerElapsedMs int64  `json:"server_elapsed_ms,omitempty"`
}

type publicResponse struct {
	Status Status       `json:"status"`
	Data   any          `json:"data,omitempty"`
	Meta   responseMeta `json:"meta"`
	Error  *ErrorBody   `json:"error,omitempty"`
}

// MarshalJSON exposes the compact public response contract. Transport and
// timing fields live under meta; OK and the legacy top-level fields never
// cross the public boundary.
func (r Response) MarshalJSON() ([]byte, error) {
	operationID := r.OperationID
	if operationID == "" && r.RequestID != "" {
		operationID = "op_" + strings.TrimPrefix(r.RequestID, "req_")
	}
	return json.Marshal(publicResponse{
		Status: r.Status,
		Data:   publicData(r.Data),
		Meta: responseMeta{
			RequestID: r.RequestID, OperationID: operationID, SessionID: r.RemoteSessionID,
			Workspace: r.Workspace, StartedAtMs: r.StartedAtMs, CompletedAtMs: r.CompletedAtMs,
			ProcessingMs: r.ProcessingMs, ServerElapsedMs: r.ServerElapsedMs,
		},
		Error: r.Error,
	})
}

// UnmarshalJSON accepts the compact public response so Go clients and tests
// can inspect the same status/meta contract that MarshalJSON emits.
func (r *Response) UnmarshalJSON(data []byte) error {
	var wire publicResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.OK = wire.Status == StatusOK
	r.Status = wire.Status
	r.Data = wire.Data
	r.Error = wire.Error
	r.RequestID = wire.Meta.RequestID
	r.OperationID = wire.Meta.OperationID
	r.RemoteSessionID = wire.Meta.SessionID
	r.Workspace = wire.Meta.Workspace
	r.StartedAtMs = wire.Meta.StartedAtMs
	r.CompletedAtMs = wire.Meta.CompletedAtMs
	r.ProcessingMs = wire.Meta.ProcessingMs
	r.ServerElapsedMs = wire.Meta.ServerElapsedMs
	return nil
}

func publicData(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if json.Unmarshal(encoded, &decoded) != nil {
		return value
	}
	return normalizePublicData(decoded)
}

func normalizePublicData(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizePublicData(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizePublicData(item)
		}
		return result
	default:
		return value
	}
}

// Accepted builds a response for work handed to a durable Task.
func Accepted(requestID, workspace string, data any) Response {
	return Response{Status: StatusAccepted, RequestID: requestID, Workspace: workspace, Data: data}
}

// Interrupted builds a response when execution ended before completion was
// observed. The caller must provide a queryable Task or operation reference.
func Interrupted(requestID, workspace string, data any) Response {
	return Response{Status: StatusInterrupted, RequestID: requestID, Workspace: workspace, Data: data}
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
		if status == StatusNeedConfirmation {
			code = "USER_CONFIRMATION_REQUIRED"
		} else if status == StatusNeedSecret && strings.Contains(strings.ToUpper(msg), "SECRET") {
			code = "SECRET_REQUIRED"
		} else if strings.Contains(strings.ToUpper(msg), "UNAUTHORIZED") {
			code = "UNAUTHORIZED"
		} else {
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
	if status == StatusNeedConfirmation || strings.Contains(code, "CONFIRMATION") {
		return "confirmation", true, "Ask the user for explicit confirmation, then retry the original tool with the same business arguments and confirmation_token."
	}
	if strings.Contains(code, "UNAUTHORIZED") || strings.Contains(code, "FORBIDDEN") || strings.Contains(code, "DENIED") || strings.Contains(code, "SECRET") {
		return "permission", false, "Request the required permission or provide the required secret."
	}
	if strings.Contains(code, "NOT_FOUND") || strings.Contains(code, "WORKSPACE_NOT_FOUND") {
		return "not_found", false, "Check the identifier and refresh the relevant list."
	}
	if strings.Contains(code, "STALE") || strings.Contains(code, "CONFLICT") || strings.Contains(code, "VERSION") || strings.Contains(code, "PATCH_CONTEXT") || strings.Contains(code, "PATCH_HUNKS") || strings.Contains(code, "PATCH_APPLY") || strings.Contains(code, "ROLLBACK") {
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
		req.OperationID = "op_" + strings.TrimPrefix(req.RequestID, "req_")
		return req, nil
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return Request{}, err
	}
	if req.Payload == nil {
		req.Payload = map[string]any{}
	}
	// purpose is the public semantic field. Intent remains an internal
	// observation name so existing handlers do not need to duplicate the value.
	if strings.TrimSpace(req.Purpose) != "" {
		req.Intent = req.Purpose
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
	if req.RemoteSessionID == "" {
		if sessionID, ok := flat["session_id"].(string); ok {
			req.RemoteSessionID = strings.TrimSpace(sessionID)
		}
	}
	for key, value := range flat {
		if key == "purpose" || key == "intent" || key == "progress_summary" || key == "session_id" || key == "remote_session_id" || key == "workspace" || key == "payload" || key == "execution_mode" || isRuntimeField(key) {
			continue
		}
		if _, exists := req.Payload[key]; !exists {
			req.Payload[key] = value
		}
	}
	req.RequestID = EnsureRequestID("")
	req.OperationID = "op_" + strings.TrimPrefix(req.RequestID, "req_")
	req.StartedAtMs = 0
	return req, nil
}

func isRuntimeField(key string) bool {
	switch key {
	case "request_id", "trace_id", "span_id", "started_at_ms", "received_at_ms", "completed_at_ms", "network_latency_ms", "processing_ms", "server_elapsed_ms", "client_info", "execution_mode":
		return true
	default:
		return false
	}
}

// Marshal serializes a Response to JSON bytes.
func Marshal(resp Response) ([]byte, error) {
	return json.Marshal(resp)
}
