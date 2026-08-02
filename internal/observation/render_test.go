package observation

import (
	"bytes"
	"strings"
	"testing"
)

func TestActionColorUsesToolAndErrorOverride(t *testing.T) {
	if got := actionColor("command_execute", false); got != ansiAmber {
		t.Fatalf("command color=%q", got)
	}
	if got := actionColor("file_read", true); got != ansiRed {
		t.Fatalf("error color=%q", got)
	}
}

func TestInteractionLineBudget(t *testing.T) {
	if maxInteractionLines != 10 || maxInteractionBodyLines != 7 {
		t.Fatalf("line budget=%d/%d", maxInteractionLines, maxInteractionBodyLines)
	}
}

func TestRenderTextHidesToolStartAndShowsHumanReadAction(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolStarted,
		Intent: "inspect the login flow",
		Input:  []byte(`{"path":"auth.go","offset":10,"limit":20}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("tool start should be hidden: %q", output.String())
	}
	if err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolCompleted,
		Intent: "inspect the login flow",
		Input:  []byte(`{"path":"auth.go","offset":10,"limit":20}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Read 1 source item(s); 42 bytes returned."}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Read auth.go", "↳ Read 1 source item(s); 42 bytes returned."} {
		if !strings.Contains(text, want) {
			t.Fatalf("human read rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "TOOL") || strings.Contains(text, "intent:") || strings.Contains(text, "```json") || strings.Contains(text, "\"path\":\"auth.go\"") {
		t.Fatalf("human read rendering leaked protocol details: %q", text)
	}
}

func TestRenderTextShowsSearchCommandAndFullMatchPaths(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "context_query",
		Type:   TypeToolCompleted,
		Intent: "查找会员查询实现",
		Input:  []byte(`{"action":"search","query":"会员卡 手机号 customer userId","paths":["fanyi-cloud-ui"],"include_glob":"**/*.vue"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Source search returned 2 match(es)."}],"structured_content":{"matches":[{"path":"fanyi-cloud-ui/src/views/erp/cashier-desk/index.vue","line":81},{"path":"fanyi-cloud-ui/src/views/erp/cashier-desk/components/order.vue","line":24}],"truncated":false}}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`• Searched rg --glob "**/*.vue" "会员卡 手机号 customer userId" fanyi-cloud-ui`,
		`↳ Source search returned 2 match(es): fanyi-cloud-ui/src/views/erp/cashier-desk/index.vue:81, fanyi-cloud-ui/src/views/erp/cashier-desk/components/order.vue:24`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("search rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "intent:") || strings.Contains(text, `"matches"`) {
		t.Fatalf("search rendering leaked intent or JSON: %s", text)
	}
}

func TestRenderTextShowsWorkspaceListResultInsteadOfOK(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "workspace_list",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"intent":"列出可用工作区"}`),
		Output: []byte(`{"status":"ok","result":{"available":true,"content":[{"type":"text","text":"{\"request_id\":\"req_1\",\"status\":\"ok\",\"data\":{\"workspaces\":[{\"name\":\"fyy\",\"path\":\"/workspaces/fyy\"}]}}"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Listed workspaces", "↳ Available workspaces: fyy (/workspaces/fyy)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("workspace list rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "intent:") || strings.Contains(text, "data=object") || strings.Contains(text, "TOOL") {
		t.Fatalf("workspace list rendering leaked protocol details: %s", text)
	}
}

func TestRenderTextShowsFindCommandForContextList(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "context_query",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"action":"list","paths":["fanyi-cloud-ui"],"include_glob":"**/*.vue"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Source list returned 2 of 2 file(s)."}],"structured_content":{"files":[{"path":"fanyi-cloud-ui/src/views/a.vue"},{"path":"fanyi-cloud-ui/src/views/b.vue"}]}}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`• Searched find fanyi-cloud-ui -type f -path "**/*.vue"`,
		`↳ Source list returned 2 of 2 file(s): fanyi-cloud-ui/src/views/a.vue, fanyi-cloud-ui/src/views/b.vue`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("context list rendering missing %q: %s", want, text)
		}
	}
}

func TestRenderTextShowsToolOutputSummaryWithoutSourceBlock(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolCompleted,
		Intent: "inspect the supplier form",
		Output: []byte(`{"status":"ok","timing":{"server_elapsed_ms":1},"result":{"content":[{"type":"text","text":"Read 1 source item(s); 42 bytes returned."}],"structured_content":{"path":"src/Supplier.vue","content":"<template>\n  <div />\n</template>\n","offset":0,"limit":3,"total_lines":3}}}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Read src/Supplier.vue", "↳ Read 1 source item(s); 42 bytes returned."} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool completion missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "### `src/Supplier.vue`") || strings.Contains(text, "```vue") || strings.Contains(text, "structured_content") || strings.Contains(text, "```json") || strings.Contains(text, "status=") {
		t.Fatalf("tool completion must render only the summary: %s", text)
	}
}

func TestRenderTextShowsExecutedCommandAndProgressSummary(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:            "command_execute",
		Type:            TypeToolCompleted,
		ProgressSummary: "已读取配置，下一步运行单元测试",
		Input:           []byte(`{"command":"go test ./internal/...","purpose":"验证修改"}`),
		Output:          []byte(`{"status":"ok","timing":{"server_elapsed_ms":12},"result":{"content":[{"type":"text","text":"Command completed with exit code 0."}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Ran go test ./internal/...", "↳ 已读取配置，下一步运行单元测试", "↳ Command completed with exit code 0."} {
		if !strings.Contains(text, want) {
			t.Fatalf("command rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "TOOL COMPLETED") || strings.Contains(text, "status=") || strings.Contains(text, "elapsed=") {
		t.Fatalf("legacy completion label leaked: %s", text)
	}
}

func TestRenderTextHidesDeprecatedSkillInput(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:  "session_open",
		Type:  TypeToolStarted,
		Input: []byte(`{"include_skills":true,"workspace":"fyy"}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if text != "" || strings.Contains(text, "include_skills") {
		t.Fatalf("tool start or deprecated skill input leaked into observation: %s", text)
	}
}

func TestRenderTextShowsMarkdownFileDiff(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Type:    TypeFileChanged,
		Summary: "update login flow",
		Output:  []byte(`{"files":[{"path":"auth.go","operation":"update","diff":"--- a/auth.go\n+++ b/auth.go\n@@\n-old\n+new\n"}],"diff":{"resource_uri":"mcpx://changeset"}}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Edited auth.go", "↳ auth.go (update) +1 -1", "-old", "+new"} {
		if !strings.Contains(text, want) {
			t.Fatalf("file diff rendering missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "mcpx://changeset") || strings.Contains(text, "```diff") || strings.Contains(text, "status=") {
		t.Fatalf("file diff rendering=%q", text)
	}
}

func TestRenderTextTruncatesLargeFileDiff(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Type:   TypeFileChanged,
		Output: []byte(`{"files":[{"path":"large.go","operation":"update","diff":"--- a/large.go\n+++ b/large.go\n@@\n-line-1\n+line-1\n-line-2\n+line-2\n-line-3\n+line-3\n-line-4\n+line-4\n-line-5\n+line-5\n"}]}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "• Edited large.go") || !strings.Contains(text, "...") {
		t.Fatalf("large diff was not summarized: %s", text)
	}
	if strings.Contains(text, "-line-5") || strings.Contains(text, "+line-5") {
		t.Fatalf("large diff leaked beyond preview: %s", text)
	}
}

func TestRenderJSONEmitsOneEventLine(t *testing.T) {
	var output bytes.Buffer
	if err := RenderJSON(&output, Event{Sequence: 7, Workspace: "demo", Type: TypeObserverNotice}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "\n") || !strings.Contains(output.String(), `"sequence":7`) {
		t.Fatalf("json rendering=%q", output.String())
	}
}
