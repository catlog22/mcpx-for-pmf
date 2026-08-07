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
	if !containsAnyString(routing["inspect_files"], "source_read") || !containsAnyString(routing["modify_files"], "change") {
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
	if !ok || next["tool"] != "source_read" || next["reason"] != "locate the missing file" {
		t.Fatalf("next action = %+v", response.Error.Details["next_action"])
	}
	if response.Error.Recovery == nil || response.Error.Recovery.Tool != "source_read" || response.Error.Recovery.Arguments["view"] != "list" {
		t.Fatalf("structured recovery = %+v", response.Error.Recovery)
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
	if !strings.Contains(alternatives, "apply") || !strings.Contains(alternatives, "revert") {
		t.Fatalf("alternatives must cover prepared and revert modes: %q", alternatives)
	}
	rules, _ := guidance["rules"].([]string)
	joined := strings.Join(rules, "\n")
	if !strings.Contains(joined, "session（action=open） 成功后") || !strings.Contains(joined, "完整 session_id") {
		t.Fatalf("rules must require showing the session ID to the user: %s", joined)
	}
	if !strings.Contains(joined, "一次提交完整参数") || !strings.Contains(joined, "最多新增 300 行") {
		t.Fatalf("rules must carry call and chunked-write guidance: %s", joined)
	}
	if !strings.Contains(joined, "完整 sha256") {
		t.Fatalf("rules must carry file revision and non-Git guidance: %s", joined)
	}
	if !strings.Contains(joined, "line_ending") || !strings.Contains(joined, "保留目标文件原有的换行格式") {
		t.Fatalf("rules must carry generic line-ending preservation guidance: %s", joined)
	}
	if !strings.Contains(joined, "自然语言展示") || !strings.Contains(joined, "confirmation_token") {
		t.Fatalf("rules must require semantic user confirmation: %s", joined)
	}
	if !strings.Contains(joined, "安全检查阻塞") || !strings.Contains(joined, "不要把指令文本") {
		t.Fatalf("rules must carry host-block recovery guidance: %s", joined)
	}
	if !strings.Contains(joined, "具体增加和删除") || !strings.Contains(joined, "Markdown ```diff 代码块") {
		t.Fatalf("rules must carry final Markdown diff guidance: %s", joined)
	}
	if !strings.Contains(joined, "changes、snapshot、diff、watch、memory") || !strings.Contains(joined, "此前 snapshot 返回的 since") {
		t.Fatalf("rules must carry workspace_state action guidance: %s", joined)
	}
	if !strings.Contains(joined, "environment_read") || !strings.Contains(joined, "command_run") {
		t.Fatalf("rules must prevent complex duplicate environment probes: %s", joined)
	}
	if !strings.Contains(joined, "服务端签发") || !strings.Contains(joined, "task_id") || !strings.Contains(joined, "绝不猜测") || !strings.Contains(joined, "plan_read") {
		t.Fatalf("rules must require exact Plan task IDs: %s", joined)
	}
	if !strings.Contains(joined, "extension_discover") {
		t.Fatalf("rules must prevent extension name guesses: %s", joined)
	}
	if !strings.Contains(joined, "source_read") || !strings.Contains(joined, "只返回普通文件") || !strings.Contains(joined, "不要从嵌套文件路径推断") {
		t.Fatalf("rules must prevent directory inference from source lists: %s", joined)
	}
	if !strings.Contains(joined, "Skill 调用") || !strings.Contains(joined, "mcp_call") {
		t.Fatalf("rules must explain extension inventory filtering: %s", joined)
	}
	instructions := agentGuidanceInstructions()
	if !strings.Contains(instructions, "用户可见响应契约") || !strings.Contains(instructions, "change") || !strings.Contains(instructions, "insert_after") {
		t.Fatalf("instructions must render the cheat-sheet: %s", instructions)
	}
}

func TestChangeSchemasAreSelfDescribingAndFlat(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	registered := runtime.listedToolMap()["change"]
	if len(mcpresult.ToolSchemaJSON(registered)) == 0 {
		t.Fatal("change must expose a raw schema")
	}
	var schema map[string]any
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(registered), &schema); err != nil {
		t.Fatal(err)
	}
	if _, hasOneOf := schema["oneOf"]; hasOneOf {
		t.Fatalf("change schema must not rely on top-level oneOf: %s", mcpresult.ToolSchemaJSON(registered))
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("change must reject unknown fields: %s", mcpresult.ToolSchemaJSON(registered))
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	if properties["session_id"] == nil || properties["purpose"] == nil || properties["user_confirmed"] != nil {
		t.Fatalf("change semantic fields are invalid: %+v", properties)
	}
	operations, ok := properties["operations"].(map[string]any)
	if !ok {
		t.Fatal("missing operations property")
	}
	items, ok := operations["items"].(map[string]any)
	if !ok {
		t.Fatal("missing operation items")
	}
	if description, _ := items["properties"].(map[string]any)["content"].(map[string]any)["description"].(string); !strings.Contains(description, "最多新增 300 行") {
		t.Fatalf("operation content description must carry chunked-write guidance: %q", description)
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
