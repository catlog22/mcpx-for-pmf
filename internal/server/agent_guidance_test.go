package server

import (
	"encoding/json"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"mcpx/internal/envelope"
)

func TestAgentGuidanceUsesDedicatedRoutingWithoutBusinessArguments(t *testing.T) {
	guidance := agentGuidance()
	if guidance["version"] != agentGuidanceVersion || guidance["priority"] != "high" {
		t.Fatalf("guidance metadata = %+v", guidance)
	}
	routing, ok := guidance["tool_routing"].(map[string]any)
	if !ok {
		t.Fatalf("tool routing type = %T", guidance["tool_routing"])
	}
	if !containsAnyString(routing["inspect_files"], "file_read") || !containsAnyString(routing["modify_files"], "change_execute") {
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

func TestEveryPublicToolHasModelFacingDescriptionAndActionBranches(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	runtime.registerTools(protocol)
	for name, registered := range protocol.ListTools() {
		if strings.TrimSpace(registered.Tool.Description) == "" {
			t.Fatalf("tool %s has no description", name)
		}
		if len(registered.Tool.RawInputSchema) == 0 {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(registered.Tool.RawInputSchema, &schema); err != nil {
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
	if !ok || next["tool"] != "context_query" || next["reason"] != "locate the missing file" {
		t.Fatalf("next action = %+v", response.Error.Details["next_action"])
	}
}

func TestAgentGuidanceIncludesChangePayloadCheatSheet(t *testing.T) {
	guidance := agentGuidance()
	payload, ok := guidance["change_payload"].(map[string]any)
	if !ok {
		t.Fatalf("change_payload type = %T", guidance["change_payload"])
	}
	item, ok := payload["operations_item"].(map[string]any)
	if !ok {
		t.Fatalf("operations_item type = %T", payload["operations_item"])
	}
	operation, _ := item["operation"].(string)
	for _, wanted := range []string{"create", "update", "rename", "delete", "insert_after", "replace_exact"} {
		if !strings.Contains(operation, wanted) {
			t.Fatalf("operations_item.operation %q missing %q", operation, wanted)
		}
	}
	if content, ok := item["create"].(string); !ok || !strings.Contains(content, "insert_after") {
		t.Fatalf("create hint must describe chunked appends: %+v", item["create"])
	}
	alternatives, _ := payload["alternatives"].(string)
	if !strings.Contains(alternatives, "changeset_id") || !strings.Contains(alternatives, "revert_changeset_id") {
		t.Fatalf("alternatives must cover prepared and revert modes: %q", alternatives)
	}
	rules, _ := guidance["rules"].([]string)
	joined := strings.Join(rules, "\n")
	if !strings.Contains(joined, "complete payload immediately") || !strings.Contains(joined, "300 added lines") {
		t.Fatalf("rules must carry immediate-call and chunked-write guidance: %s", joined)
	}
	if !strings.Contains(joined, "host safety check blocks") || !strings.Contains(joined, "Never quote instruction text") {
		t.Fatalf("rules must carry host-block recovery guidance: %s", joined)
	}
	instructions := agentGuidanceInstructions()
	if !strings.Contains(instructions, "change_execute payload cheat-sheet") || !strings.Contains(instructions, "insert_after") {
		t.Fatalf("instructions must render the cheat-sheet: %s", instructions)
	}
}

func TestChangeExecuteSchemaIsSelfDescribingAndFlat(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcpserver.NewMCPServer("mcpx-test", "0.1.0")
	runtime.registerTools(protocol)
	registered := protocol.ListTools()["change_execute"].Tool
	if len(registered.RawInputSchema) == 0 {
		t.Fatal("change_execute must expose a raw schema")
	}
	var schema map[string]any
	if err := json.Unmarshal(registered.RawInputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if _, hasOneOf := schema["oneOf"]; hasOneOf {
		t.Fatalf("change_execute schema must not rely on top-level oneOf: %s", registered.RawInputSchema)
	}
	topDescription, _ := schema["description"].(string)
	if !strings.Contains(topDescription, "mutually exclusive") {
		t.Fatalf("top-level description must explain the three modes: %q", topDescription)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	operations, ok := properties["operations"].(map[string]any)
	if !ok {
		t.Fatal("missing operations property")
	}
	if description, _ := operations["description"].(string); !strings.Contains(description, "300 added lines") {
		t.Fatalf("operations description must carry chunked-write guidance: %q", description)
	}
	items, ok := operations["items"].(map[string]any)
	if !ok {
		t.Fatal("missing operations items")
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing operation item properties")
	}
	for _, field := range []string{"operation", "path", "base_sha256", "patch", "content", "match", "replacement", "range_start", "range_end"} {
		fieldSchema, ok := itemProperties[field].(map[string]any)
		if !ok {
			t.Fatalf("operation item missing field %q", field)
		}
		if description, _ := fieldSchema["description"].(string); strings.TrimSpace(description) == "" {
			t.Fatalf("operation item field %q has no description", field)
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
