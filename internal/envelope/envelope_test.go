package envelope

import (
	"encoding/json"
	"testing"
)

func TestOK(t *testing.T) {
	r := OK("req1", "ws", map[string]any{"x": 1})
	if !r.OK || r.Status != StatusOK || r.RequestID != "req1" || r.Workspace != "ws" {
		t.Fatalf("unexpected OK response: %+v", r)
	}
	if r.Error != nil {
		t.Fatalf("error should be nil")
	}
}

func TestFail(t *testing.T) {
	r := Fail(StatusDenied, "req1", "ws", nil, "denied", "blocked")
	if r.OK || r.Status != StatusDenied {
		t.Fatalf("unexpected Fail: %+v", r)
	}
	if r.Error == nil || r.Error.Code != "DENIED" || r.Error.Category != "permission" || r.Error.Details == nil {
		t.Fatalf("expected full error contract, got %+v", r.Error)
	}
}

func TestMarshalUsesSingleStatusAndCompactMeta(t *testing.T) {
	response := OK("req_1", "demo", map[string]any{"session_id": "rs_1", "value": "ready"})
	response.RemoteSessionID = "rs_1"
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "succeeded" || payload["ok"] != nil || payload["remote_session_id"] != nil {
		t.Fatalf("public response leaked legacy status fields: %s", encoded)
	}
	meta, _ := payload["meta"].(map[string]any)
	if meta["request_id"] != "req_1" || meta["operation_id"] != "op_1" || meta["session_id"] != "rs_1" {
		t.Fatalf("response meta = %+v", meta)
	}
}

func TestEnsureRequestID(t *testing.T) {
	if EnsureRequestID("keep") != "keep" {
		t.Fatal("should keep existing id")
	}
	id := EnsureRequestID("")
	if id == "" || len(id) < 8 {
		t.Fatalf("generated id too short: %q", id)
	}
	id2 := EnsureRequestID("")
	if id == id2 {
		t.Fatal("expected unique ids")
	}
}

func TestParseRequest(t *testing.T) {
	raw := json.RawMessage(`{"request_id":"r1","trace_id":"tr1","started_at_ms":123,"workspace":"p","payload":{"command":"ls","span_id":"sp1"}}`)
	req, err := ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.RequestID == "r1" || req.RequestID == "" || req.Workspace != "p" {
		t.Fatalf("runtime context fields must not come from arguments: %+v", req)
	}
	if req.Payload["command"] != "ls" || req.Payload["request_id"] != nil || req.Payload["trace_id"] != nil || req.Payload["span_id"] != nil {
		t.Fatalf("runtime fields leaked into payload: %+v", req.Payload)
	}

	req2, err := ParseRequest(nil)
	if err != nil {
		t.Fatal(err)
	}
	if req2.RequestID == "" || req2.Payload == nil {
		t.Fatalf("empty parse: %+v", req2)
	}
}

func TestParseRequestMergesFlatArgumentsIntoPayload(t *testing.T) {
	req, err := ParseRequest(json.RawMessage(`{"intent":"inspect workspace","progress_summary":"已完成定位，下一步读取文件","workspace":"demo","command":"pwd","payload":{"command":"echo legacy"},"task_id":"t1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Payload["command"] != "echo legacy" {
		t.Fatalf("payload should win: %+v", req.Payload)
	}
	if req.Payload["task_id"] != "t1" {
		t.Fatalf("flat argument missing: %+v", req.Payload)
	}
	if req.Intent != "inspect workspace" {
		t.Fatalf("intent missing: %q", req.Intent)
	}
	if req.ProgressSummary != "已完成定位，下一步读取文件" {
		t.Fatalf("progress summary missing: %q", req.ProgressSummary)
	}
	if _, exists := req.Payload["intent"]; exists {
		t.Fatalf("intent leaked into payload: %+v", req.Payload)
	}
	if _, exists := req.Payload["progress_summary"]; exists {
		t.Fatalf("progress summary leaked into payload: %+v", req.Payload)
	}
}
