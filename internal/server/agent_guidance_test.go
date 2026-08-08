package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"encoding/json"
	"strings"
	"testing"

	"mcpx/internal/envelope"
)

func TestAgentGuidanceUsesDedicatedRoutingWithoutBusinessArguments(t *testing.T) {
	guidance := agentGuidance()
	if guidance["version"] != agentGuidanceVersion || guidance["priority"] != "high" {
		t.Fatalf("guidance metadata = %+v", guidance)
	}
	routingIface, ok := guidance["tool_routing"]
	if !ok {
		t.Fatalf("tool routing type = %T", guidance["tool_routing"])
	}
	var routing map[string]any
	if m, ok := routingIface.(map[string]any); ok {
		routing = m
	} else if m, ok := routingIface.(map[string][]string); ok {
		routing = make(map[string]any, len(m))
		for k, v := range m {
			routing[k] = v
		}
	} else {
		t.Fatalf("tool routing type = %T", guidance["tool_routing"])
	}
	if !containsAnyString(routing["inspect_files"], "read") || !containsAnyString(routing["modify_files"], "edit") {
		t.Fatalf("guidance routing = %+v", routing)
	}
	for _, forbidden := range []string{"presentation", "renderer", "show_source", "density"} {
		if strings.Contains(strings.ToLower(mustJSON(t, guidance)), `"`+forbidden+`"`) {
			t.Fatalf("guidance contains host presentation argument %q", forbidden)
		}
	}
	revision := agentGuidanceRevision()
	if revision == "" || revision != agentGuidanceRevision() {
		t.Fatalf("guidance revision is not stable: %q", revision)
	}
}

func TestAgentGuidanceRequiresUserVisibleResponseContract(t *testing.T) {
	guidance := agentGuidance()
	contract, ok := guidance["response_contract"].(map[string]any)
	if !ok || contract["required"] != true {
		t.Fatalf("response contract = %+v", guidance["response_contract"])
	}
	for _, field := range []string{"before_tool_call", "after_tool_call", "final_response"} {
		items, ok := contract[field].([]string)
		if !ok || len(items) == 0 {
			t.Fatalf("response contract field %q = %+v", field, contract[field])
		}
	}
	after, _ := contract["after_tool_call"].([]string)
	joinedAfter := strings.Join(after, "\n")
	if !strings.Contains(joinedAfter, "progress_summary") || !strings.Contains(joinedAfter, "下一步") {
		t.Fatalf("after-tool progress contract is incomplete: %+v", after)
	}
	evidence, _ := contract["evidence_rule"].(string)
	if !strings.Contains(evidence, "不得声称") || !strings.Contains(evidence, "工具结果") {
		t.Fatalf("evidence rule = %q", evidence)
	}
}

func TestEveryPublicToolHasModelFacingDescriptionAndActionBranches(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	for name, registered := range runtime.listedToolMap() {
		if strings.TrimSpace(registered.Description) == "" {
			t.Fatalf("tool %s has no description", name)
		}
		if len(mcpresult.ToolSchemaJSON(registered)) == 0 {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(mcpresult.ToolSchemaJSON(registered), &schema); err != nil {
			t.Fatalf("tool %s schema: %v", name, err)
		}
		branches, ok := schema["oneOf"].([]any)
		if !ok {
			continue
		}
		for index, branch := range branches {
			item, ok := branch.(map[string]any)
			description, descriptionOK := item["description"].(string)
			if !ok || !descriptionOK || strings.TrimSpace(description) == "" {
				t.Fatalf("tool %s branch %d has no description: %+v", name, index, branch)
			}
		}
	}
}

func TestRecoveryActionIsStructuredInErrorDetails(t *testing.T) {
	response := envelope.Fail(envelope.StatusError, "req_test", "demo", nil, "NOT_FOUND", "missing")
	addRecoveryAction(&response, "context_query", "locate the missing file", map[string]any{"action": "list"})
	if response.Error == nil {
		t.Fatal("missing error body")
	}
	next, ok := response.Error.Details["next_action"].(map[string]any)
	if !ok || next["tool"] != "read" || next["reason"] != "locate the missing file" {
		t.Fatalf("next action = %+v", response.Error.Details["next_action"])
	}
	if response.Error.Recovery == nil || response.Error.Recovery.Tool != "read" || response.Error.Recovery.Arguments["view"] != "list" {
		t.Fatalf("structured recovery = %+v", response.Error.Recovery)
	}
}

func TestAgentGuidanceIncludesEditPayloadCheatSheet(t *testing.T) {
	guidance := agentGuidance()
	payload, ok := guidance["edit_payload"].(map[string]any)
	if !ok {
		t.Fatalf("edit_payload type = %T", guidance["edit_payload"])
	}
	if payload["tool"] != "edit" || !containsAnyString(payload["required"], "remote_session_id") || !containsAnyString(payload["required"], "edits") {
		t.Fatalf("edit payload contract = %+v", payload)
	}
	item, ok := payload["edit_item"].(map[string]any)
	if !ok {
		t.Fatalf("edit_item type = %T", payload["edit_item"])
	}
	operation, _ := item["operation"].(string)
	for _, wanted := range []string{"create", "update", "rename", "delete"} {
		if !strings.Contains(operation, wanted) {
			t.Fatalf("edit_item.operation %q missing %q", operation, wanted)
		}
	}
	replacement, ok := item["replacement"].(string)
	if !ok || !strings.Contains(replacement, "精确唯一") {
		t.Fatalf("replacement hint must describe exact matching: %+v", item["replacement"])
	}
	limit, _ := item["limit"].(string)
	if !strings.Contains(limit, "1000") {
		t.Fatalf("edit limit must be strict 1000 lines: %q", limit)
	}
	rules, _ := guidance["rules"].([]string)
	joined := strings.Join(rules, "\n")
	if !strings.Contains(joined, "session（action=open）成功后") || !strings.Contains(joined, "完整的 remote_session_id") {
		t.Fatalf("rules must require showing the session ID to the user: %s", joined)
	}
	if !strings.Contains(joined, "使用 read") || !strings.Contains(joined, "使用 edit") || !strings.Contains(joined, "不得超过 1000 行") {
		t.Fatalf("rules must carry clean-core read/edit guidance: %s", joined)
	}
	if !strings.Contains(joined, "完整 sha256") || !strings.Contains(joined, "line_ending") {
		t.Fatalf("rules must carry file revision and non-Git guidance: %s", joined)
	}
	if !strings.Contains(joined, "idempotency_key") || !strings.Contains(joined, "STALE_REVISION") || !strings.Contains(joined, "suggested_next") {
		t.Fatalf("rules must carry edit retry guidance: %s", joined)
	}
	instructions := agentGuidanceInstructions()
	if !strings.Contains(instructions, "用户可见响应契约") || !strings.Contains(instructions, "edit") || !strings.Contains(instructions, "replacement") {
		t.Fatalf("instructions must render the cheat-sheet: %s", instructions)
	}
}

func TestEditSchemaIsSelfDescribingAndFlat(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	registered := runtime.listedToolMap()["edit"]
	if len(mcpresult.ToolSchemaJSON(registered)) == 0 {
		t.Fatal("edit must expose a raw schema")
	}
	var schema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(registered), &schema); err != nil {
		t.Fatal(err)
	}
	if _, hasOneOf := schema["oneOf"]; hasOneOf {
		t.Fatalf("edit schema must not rely on top-level oneOf: %s", mcpresult.ToolSchemaJSON(registered))
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("edit must reject unknown fields: %s", mcpresult.ToolSchemaJSON(registered))
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	if properties["remote_session_id"] == nil || properties["purpose"] == nil || properties["idempotency_key"] == nil {
		t.Fatalf("edit semantic fields are invalid: %+v", properties)
	}
	edits, ok := properties["edits"].(map[string]any)
	if !ok {
		t.Fatal("missing edits property")
	}
	items, ok := edits["items"].(map[string]any)
	if !ok {
		t.Fatal("missing edit items")
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing edit item properties")
	}
	for _, field := range []string{"operation", "path", "base_sha256", "content", "new_path", "replacements"} {
		fieldSchema, ok := itemProperties[field].(map[string]any)
		if !ok {
			t.Fatalf("edit item missing field %q", field)
		}
		if description, _ := fieldSchema["description"].(string); strings.TrimSpace(description) == "" {
			t.Fatalf("edit item field %q has no description", field)
		}
	}
	replacements := itemProperties["replacements"].(map[string]any)
	replacementItems := replacements["items"].(map[string]any)
	for _, field := range []string{"match", "replacement"} {
		fieldSchema := replacementItems["properties"].(map[string]any)[field].(map[string]any)
		if strings.TrimSpace(fieldSchema["description"].(string)) == "" {
			t.Fatalf("replacement field %q has no description", field)
		}
	}
}

func containsAnyString(value any, wanted string) bool {
	switch items := value.(type) {
	case []string:
		for _, item := range items {
			if item == wanted {
				return true
			}
		}
	case []any:
		for _, item := range items {
			if item, ok := item.(string); ok && item == wanted {
				return true
			}
		}
	}
	return false
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
