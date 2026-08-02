package arc

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	maxHumanItems        = 20
	maxHumanTools        = 24
	maxHumanSnippetLines = 14
	maxHumanSnippetRunes = 1800
	maxHumanTextRunes    = 280
)

// RenderToolContent renders the model-facing part of a tool result. The ARC
// envelope remains machine-readable in response metadata; this function keeps
// the default text useful when an MCP host does not render structuredContent.
func RenderToolContent(tool, resultType, renderer, summary string, data any) (string, bool) {
	if resultType == "code_change" && renderer == "diff" {
		return RenderContent(resultType, renderer, summary, data)
	}
	asMap, ok := data.(map[string]any)
	if !ok {
		return summary, false
	}

	switch tool {
	case "workspace_list":
		return renderWorkspaceList(summary, asMap)
	case "session_open":
		return renderSessionOpen(summary, asMap)
	case "context_query":
		return renderContextQuery(summary, asMap)
	case "artifact_manage":
		if text, ok := renderArtifactRead(summary, asMap); ok {
			return text, true
		}
	}

	if text, ok := renderProjectData(summary, asMap); ok {
		return text, true
	}
	if text, ok := renderKnownCollection(summary, asMap); ok {
		return text, true
	}
	if tool == "session_manage" || tool == "remote_session_get" {
		if text, ok := renderSessionSummary(summary, asMap); ok {
			return text, true
		}
	}
	if text, ok := renderGenericData(summary, asMap); ok {
		return text, true
	}
	return summary, false
}

func renderWorkspaceList(summary string, data map[string]any) (string, bool) {
	value, exists := data["workspaces"]
	if !exists {
		return summary, false
	}
	items := humanMaps(value)
	if len(items) == 0 {
		return "No registered workspaces.", true
	}

	var builder strings.Builder
	writeHumanSummary(&builder, summary)
	if builder.Len() > 0 {
		builder.WriteString("\n\n")
	}
	builder.WriteString("Available workspaces:")
	for index, item := range items {
		if index >= maxHumanItems {
			break
		}
		name := humanField(item, "name")
		path := humanField(item, "path")
		if name == "" {
			name = "unnamed workspace"
		}
		if path == "" {
			path = "path unavailable"
		}
		fmt.Fprintf(&builder, "\n- %s — %s", inlineCode(name), inlineCode(path))
		if description := compactHuman(humanField(item, "description")); description != "" {
			builder.WriteString(" — ")
			builder.WriteString(description)
		}
	}
	appendMoreItems(&builder, len(items), maxHumanItems)
	return strings.TrimSpace(builder.String()), true
}

func renderSessionOpen(summary string, data map[string]any) (string, bool) {
	remote := humanMap(data["remote_session"])
	workspace := humanMap(data["workspace"])
	if len(remote) == 0 && len(workspace) == 0 && data["agent_guidance"] == nil && data["instructions"] == nil {
		return summary, false
	}

	var builder strings.Builder
	writeHumanSummary(&builder, summary)
	if builder.Len() == 0 {
		builder.WriteString("Session opened.")
	}
	if id := humanField(remote, "id"); id != "" {
		fmt.Fprintf(&builder, "\n\n- Remote session: %s", inlineCode(id))
	}
	if role := humanField(remote, "role"); role != "" {
		fmt.Fprintf(&builder, "\n- Role: `%s`", role)
	}
	if status := humanField(remote, "status"); status != "" {
		fmt.Fprintf(&builder, "\n- Status: `%s`", status)
	}
	name := humanField(workspace, "name")
	path := humanField(workspace, "path")
	if name != "" || path != "" {
		builder.WriteString("\n- Workspace: ")
		if name != "" {
			builder.WriteString(inlineCode(name))
		}
		if path != "" {
			if name != "" {
				builder.WriteString(" — ")
			}
			builder.WriteString(inlineCode(path))
		}
	}
	if head := humanField(workspace, "git_head"); head != "" {
		fmt.Fprintf(&builder, "\n- Git head: `%s`", head)
	}

	if tools := humanNames(data["tools"], "name"); len(tools) > 0 {
		builder.WriteString("\n- Available tools: ")
		builder.WriteString(joinInlineCodes(tools, maxHumanTools))
	}
	appendSessionInventory(&builder, "Skills", data["skills"])
	appendSessionInventory(&builder, "MCP servers", data["upstream_mcp"])
	appendSessionInventory(&builder, "Active changesets", data["active_changesets"])
	appendSessionInventory(&builder, "Pending approvals", data["pending_approvals"])
	appendSessionInventory(&builder, "Tasks", data["tasks"])
	appendSessionInventory(&builder, "Artifacts", data["artifacts"])
	appendRevisionFacts(&builder, data["revisions"])
	appendRefreshFacts(&builder, data["client_refresh"])
	appendGuidance(&builder, humanMap(data["agent_guidance"]))
	appendInstructions(&builder, data["instructions"])
	return strings.TrimSpace(builder.String()), true
}

func renderSessionSummary(summary string, data map[string]any) (string, bool) {
	if humanField(data, "id") == "" && humanField(data, "workspace_name") == "" {
		return summary, false
	}
	var builder strings.Builder
	writeHumanSummary(&builder, summary)
	if builder.Len() == 0 {
		builder.WriteString("Remote session:")
	}
	for _, field := range []struct {
		label string
		key   string
	}{
		{label: "ID", key: "id"},
		{label: "Workspace", key: "workspace_name"},
		{label: "Status", key: "status"},
		{label: "Role", key: "role"},
	} {
		if value := humanField(data, field.key); value != "" {
			fmt.Fprintf(&builder, "\n- %s: %s", field.label, inlineCode(value))
		}
	}
	return strings.TrimSpace(builder.String()), true
}

func renderContextQuery(summary string, data map[string]any) (string, bool) {
	_, hasMatches := data["matches"]
	_, hasFiles := data["files"]
	if !hasMatches && !hasFiles {
		return summary, false
	}

	var builder strings.Builder
	writeHumanSummary(&builder, summary)
	if builder.Len() == 0 {
		builder.WriteString("Context query results:")
	}

	if hasMatches {
		matches := humanMaps(data["matches"])
		for index, match := range matches {
			if index >= maxHumanItems {
				break
			}
			path := humanField(match, "path")
			location := path
			if line := humanField(match, "line"); line != "" {
				location += ":" + line
				if column := humanField(match, "column"); column != "" {
					location += ":" + column
				}
			}
			if location == "" {
				location = "match"
			}
			lineText := compactHuman(humanField(match, "text"))
			builder.WriteString("\n- ")
			builder.WriteString(inlineCode(location))
			if lineText != "" {
				builder.WriteString(" — ")
				builder.WriteString(lineText)
			}
		}
		appendMoreItems(&builder, len(matches), maxHumanItems)
	}

	if hasFiles {
		files := humanMaps(data["files"])
		shownSnippets := 0
		for index, file := range files {
			if index >= maxHumanItems {
				break
			}
			path := humanField(file, "path")
			if path == "" {
				continue
			}
			builder.WriteString("\n- ")
			builder.WriteString(inlineCode(path))
			content := humanField(file, "content")
			if content == "" || shownSnippets >= 4 {
				continue
			}
			shownSnippets++
			language := humanLanguage(path)
			snippet, truncated := humanSnippet(content)
			fmt.Fprintf(&builder, "\n\n  ```%s\n%s\n  ```", language, indentSnippet(snippet))
			if truncated {
				builder.WriteString("\n  ...")
			}
		}
		appendMoreItems(&builder, len(files), maxHumanItems)
	}
	return strings.TrimSpace(builder.String()), true
}

func renderArtifactRead(summary string, data map[string]any) (string, bool) {
	content := humanField(data, "data")
	if content == "" {
		return summary, false
	}
	artifact := humanMap(data["artifact"])
	var builder strings.Builder
	writeHumanSummary(&builder, summary)
	if builder.Len() == 0 {
		builder.WriteString("Artifact content:")
	}
	if path := humanField(artifact, "path"); path != "" {
		fmt.Fprintf(&builder, "\n\n`%s`", path)
	}
	if humanField(data, "encoding") == "base64" {
		builder.WriteString("\n\n```text\n")
		builder.WriteString(compactHuman(content))
		builder.WriteString("\n```")
		return strings.TrimSpace(builder.String()), true
	}
	snippet, truncated := humanSnippet(content)
	fmt.Fprintf(&builder, "\n\n```%s\n%s\n```", humanLanguage(humanField(artifact, "path")), snippet)
	if truncated {
		builder.WriteString("\n\n... artifact content continues; use artifact_manage read for the next offset")
	}
	return strings.TrimSpace(builder.String()), true
}

func renderKnownCollection(summary string, data map[string]any) (string, bool) {
	for _, key := range []string{"sessions", "artifacts", "changesets", "tasks", "approvals", "events", "changes", "files", "tools", "documents", "capabilities", "skills", "upstream_mcp", "servers"} {
		value, exists := data[key]
		if !exists {
			continue
		}
		items := value
		if nested := humanMap(value); nested != nil {
			if candidate, ok := nested["items"]; ok {
				items = candidate
			}
		}
		list := humanMaps(items)
		var builder strings.Builder
		hasSummary := !isGenericSummary(summary)
		writeHumanSummary(&builder, summary)
		if !hasSummary {
			builder.WriteString(humanCollectionTitle(key))
		}
		if len(list) == 0 {
			if key == "skills" || key == "servers" || key == "upstream_mcp" {
				builder.WriteString("\n- None")
			}
			return strings.TrimSpace(builder.String()), true
		}
		if hasSummary {
			builder.WriteString("\n\n")
			builder.WriteString(humanCollectionTitle(key))
		}
		for index, item := range list {
			if index >= maxHumanItems {
				break
			}
			builder.WriteString("\n- ")
			builder.WriteString(humanCollectionItem(item))
		}
		appendMoreItems(&builder, len(list), maxHumanItems)
		return strings.TrimSpace(builder.String()), true
	}
	return summary, false
}

func renderProjectData(summary string, data map[string]any) (string, bool) {
	if _, hasStacks := data["stacks"]; !hasStacks {
		if _, hasManifests := data["manifests"]; !hasManifests {
			return summary, false
		}
	}
	var builder strings.Builder
	writeHumanSummary(&builder, summary)
	if builder.Len() == 0 {
		builder.WriteString("Project summary:")
	}
	for _, key := range []string{"stacks", "manifests", "entrypoints", "instructions"} {
		if values := humanStrings(data[key]); len(values) > 0 {
			fmt.Fprintf(&builder, "\n- %s: %s", key, joinInlineCodes(values, maxHumanItems))
		}
	}
	if tasks := humanMap(data["tasks"]); len(tasks) > 0 {
		builder.WriteString("\n- Tasks:")
		keys := make([]string, 0, len(tasks))
		for key := range tasks {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&builder, "\n  - %s: %s", inlineCode(key), compactHuman(fmt.Sprint(tasks[key])))
		}
	}
	if status := humanField(data, "git_status"); status != "" {
		builder.WriteString("\n- Git status:\n\n```text\n")
		builder.WriteString(status)
		builder.WriteString("\n```")
	}
	return strings.TrimSpace(builder.String()), true
}

func renderGenericData(summary string, data map[string]any) (string, bool) {
	if !isGenericSummary(summary) || len(data) == 0 {
		return summary, false
	}
	if len(data) == 1 {
		if text, ok := data["text"].(string); ok && strings.TrimSpace(text) == strings.TrimSpace(summary) {
			return summary, false
		}
	}
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("Result:")
	for _, key := range keys {
		if key == "next_action" {
			continue
		}
		line, block := genericField(key, data[key])
		if line == "" && block == "" {
			continue
		}
		if block != "" {
			fmt.Fprintf(&builder, "\n- %s:\n\n```%s\n%s\n```", key, humanLanguage(key), block)
			continue
		}
		builder.WriteString("\n- ")
		builder.WriteString(line)
	}
	if builder.String() == "Result:" {
		return summary, false
	}
	return strings.TrimSpace(builder.String()), true
}

func genericField(key string, value any) (string, string) {
	switch typed := value.(type) {
	case string:
		if strings.Contains(typed, "\n") && isBlockField(key) {
			snippet, _ := humanSnippetWithLimit(typed, maxHumanSnippetLines, maxHumanSnippetRunes)
			return "", snippet
		}
		return key + ": " + inlineCode(compactHuman(typed)), ""
	case bool, float64, int, int64:
		return key + ": " + inlineCode(fmt.Sprint(typed)), ""
	case []string:
		return key + ": " + joinInlineCodes(typed, maxHumanItems), ""
	case []any, []map[string]any:
		items := humanMaps(typed)
		if len(items) > 0 {
			return key + ": " + humanCollectionItem(items[0]) + appendItemCount(len(items)), ""
		}
		return key + ": " + inlineCode("0 items"), ""
	case map[string]any:
		if summary := humanObjectSummary(typed); summary != "" {
			return key + ": " + summary, ""
		}
		return key + ": " + inlineCode("object"), ""
	default:
		if mapped := humanMap(typed); mapped != nil {
			return key + ": " + inlineCode("object"), ""
		}
		return key + ": " + inlineCode(compactHuman(fmt.Sprint(typed))), ""
	}
}

func humanObjectSummary(value map[string]any) string {
	parts := make([]string, 0, 4)
	for _, key := range []string{"name", "id", "path", "status", "state", "kind", "encoding"} {
		if field := humanField(value, key); field != "" {
			parts = append(parts, key+"="+inlineCode(field))
		}
	}
	return strings.Join(parts, " ")
}

func appendItemCount(count int) string {
	if count <= 1 {
		return ""
	}
	return fmt.Sprintf(" (+%d more)", count-1)
}

func isBlockField(key string) bool {
	switch key {
	case "content", "stdout", "stderr", "diff", "markdown", "text", "git_status":
		return true
	default:
		return false
	}
}

func isGenericSummary(summary string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" || strings.EqualFold(summary, "ok") || strings.EqualFold(summary, "result") {
		return true
	}
	var value any
	return json.Unmarshal([]byte(summary), &value) == nil
}

func appendSessionInventory(builder *strings.Builder, title string, value any) {
	if value == nil {
		return
	}
	container := humanMap(value)
	itemsValue := value
	enabled := ""
	if container != nil {
		if raw, ok := container["items"]; ok {
			itemsValue = raw
		}
		if raw, ok := container["servers"]; ok {
			itemsValue = raw
		}
		if raw, ok := container["enabled"]; ok {
			enabled = strings.ToLower(fmt.Sprint(raw))
		}
	}
	items := humanMaps(itemsValue)
	builder.WriteString("\n\n- ")
	builder.WriteString(title)
	if enabled != "" {
		builder.WriteString(" (")
		builder.WriteString(enabled)
		builder.WriteString(")")
	}
	builder.WriteString(":")
	if len(items) == 0 {
		builder.WriteString(" none")
		return
	}
	for index, item := range items {
		if index >= maxHumanItems {
			break
		}
		builder.WriteString("\n  - ")
		builder.WriteString(humanCollectionItem(item))
	}
	appendMoreItems(builder, len(items), maxHumanItems)
}

func appendRevisionFacts(builder *strings.Builder, value any) {
	data := humanMap(value)
	if len(data) == 0 {
		return
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"tool_schema_revision", "guidance_revision", "session_capability_revision"} {
		if field := humanField(data, key); field != "" {
			parts = append(parts, key+"="+inlineCode(field))
		}
	}
	if len(parts) > 0 {
		builder.WriteString("\n\n- Revisions: ")
		builder.WriteString(strings.Join(parts, ", "))
	}
}

func appendRefreshFacts(builder *strings.Builder, value any) {
	data := humanMap(value)
	if len(data) == 0 {
		return
	}
	required := strings.ToLower(humanField(data, "required"))
	reason := humanField(data, "reason")
	if required == "" && reason == "" {
		return
	}
	builder.WriteString("\n\n- Client refresh: ")
	if required != "" {
		builder.WriteString(required)
	}
	if reason != "" {
		builder.WriteString(" (" + reason + ")")
	}
}

func appendGuidance(builder *strings.Builder, guidance map[string]any) {
	if len(guidance) == 0 {
		return
	}
	builder.WriteString("\n\nAgent guidance")
	if summary := compactHuman(humanField(guidance, "summary")); summary != "" {
		builder.WriteString("\n")
		builder.WriteString(summary)
	}
	if rules := humanStrings(guidance["rules"]); len(rules) > 0 {
		builder.WriteString("\n\nRules:")
		for _, rule := range rules {
			builder.WriteString("\n- ")
			builder.WriteString(rule)
		}
	}
	contract := humanMap(guidance["response_contract"])
	for _, section := range []string{"before_tool_call", "after_tool_call", "final_response"} {
		items := humanStrings(contract[section])
		if len(items) == 0 {
			continue
		}
		builder.WriteString("\n\n")
		builder.WriteString(humanContractTitle(section))
		for _, item := range items {
			builder.WriteString("\n- ")
			builder.WriteString(item)
		}
	}
	if evidence := compactHuman(humanField(contract, "evidence_rule")); evidence != "" {
		builder.WriteString("\n- Evidence rule: ")
		builder.WriteString(evidence)
	}
}

func appendInstructions(builder *strings.Builder, value any) {
	container := humanMap(value)
	if container == nil {
		return
	}
	documents := humanMaps(container["documents"])
	if len(documents) == 0 {
		return
	}
	builder.WriteString("\n\nInstructions:")
	for index, document := range documents {
		if index >= 8 {
			break
		}
		name := humanField(document, "name")
		if name == "" {
			name = humanField(document, "id")
		}
		builder.WriteString("\n- ")
		builder.WriteString(inlineCode(name))
		if active, ok := document["active"].(bool); ok && active {
			builder.WriteString(" (active)")
		}
		content := humanField(document, "content")
		if content == "" {
			continue
		}
		snippet, truncated := humanSnippetWithLimit(content, 80, 12000)
		builder.WriteString("\n\n  ```markdown\n")
		builder.WriteString(indentSnippet(snippet))
		builder.WriteString("\n  ```")
		if truncated {
			builder.WriteString("\n  ... instruction content truncated")
		}
	}
}

func writeHumanSummary(builder *strings.Builder, summary string) {
	summary = strings.TrimSpace(summary)
	if summary == "" || strings.EqualFold(summary, "ok") || strings.EqualFold(summary, "result") {
		return
	}
	builder.WriteString(summary)
}

func humanMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if result, ok := value.(map[string]any); ok {
		return result
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result map[string]any
	if json.Unmarshal(encoded, &result) != nil {
		return nil
	}
	return result
}

func humanMaps(value any) []map[string]any {
	if value == nil {
		return nil
	}
	if items, ok := value.([]map[string]any); ok {
		return items
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var items []map[string]any
	if json.Unmarshal(encoded, &items) != nil {
		return nil
	}
	return items
}

func humanStrings(value any) []string {
	if value == nil {
		return nil
	}
	if items, ok := value.([]string); ok {
		return items
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var items []string
	if json.Unmarshal(encoded, &items) != nil {
		return nil
	}
	return items
}

func humanNames(value any, key string) []string {
	items := humanMaps(value)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if name := humanField(item, key); name != "" {
			result = append(result, name)
		}
	}
	return result
}

func humanField(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func compactHuman(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len([]rune(value)) <= maxHumanTextRunes {
		return value
	}
	return string([]rune(value)[:maxHumanTextRunes-1]) + "…"
}

func humanSnippet(value string) (string, bool) {
	return humanSnippetWithLimit(value, maxHumanSnippetLines, maxHumanSnippetRunes)
}

func humanSnippetWithLimit(value string, maxLines, maxRunes int) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var builder strings.Builder
	shown := 0
	used := 0
	truncated := false
	for _, line := range lines {
		if shown >= maxLines || used+len([]rune(line)) > maxRunes {
			truncated = true
			break
		}
		if shown > 0 {
			builder.WriteByte('\n')
			used++
		}
		builder.WriteString(line)
		used += len([]rune(line))
		shown++
	}
	if shown < len(lines) {
		truncated = true
	}
	return builder.String(), truncated
}

func indentSnippet(value string) string {
	return strings.ReplaceAll(value, "\n", "\n  ")
}

func inlineCode(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "`", "'")
	return "`" + value + "`"
}

func joinInlineCodes(values []string, limit int) string {
	if len(values) > limit {
		values = append(append([]string(nil), values[:limit]...), fmt.Sprintf("... +%d more", len(values)-limit))
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, inlineCode(value))
	}
	return strings.Join(parts, ", ")
}

func appendMoreItems(builder *strings.Builder, total, limit int) {
	if total > limit {
		fmt.Fprintf(builder, "\n- ... +%d more", total-limit)
	}
}

func humanLanguage(path string) string {
	lower := strings.ToLower(path)
	for suffix, language := range map[string]string{
		".go": "go", ".vue": "vue", ".ts": "typescript", ".tsx": "tsx", ".js": "javascript",
		".jsx": "jsx", ".json": "json", ".yaml": "yaml", ".yml": "yaml", ".md": "markdown",
		".css": "css", ".scss": "scss", ".html": "html", ".sql": "sql",
	} {
		if strings.HasSuffix(lower, suffix) {
			return language
		}
	}
	return "text"
}

func humanCollectionTitle(key string) string {
	return map[string]string{
		"sessions":     "Remote sessions:",
		"artifacts":    "Artifacts:",
		"changesets":   "Changesets:",
		"tasks":        "Tasks:",
		"approvals":    "Approvals:",
		"events":       "Events:",
		"changes":      "Changes:",
		"files":        "Files:",
		"tools":        "Tools:",
		"documents":    "Instructions:",
		"capabilities": "Capabilities:",
		"skills":       "Skills:",
		"upstream_mcp": "MCP servers:",
		"servers":      "Servers:",
	}[key]
}

func humanCollectionItem(item map[string]any) string {
	label := "item"
	for _, key := range []string{"name", "id", "session_id", "task_id", "artifact_id", "changeset_id", "approval_id", "path", "type", "summary"} {
		if value := humanField(item, key); value != "" {
			label = value
			break
		}
	}
	parts := []string{inlineCode(label)}
	for _, key := range []string{"status", "state", "operation", "workspace_name", "path"} {
		if value := humanField(item, key); value != "" && value != label {
			parts = append(parts, fmt.Sprintf("%s=%s", key, inlineCode(value)))
		}
	}
	if summary := compactHuman(humanField(item, "summary")); summary != "" && summary != label {
		parts = append(parts, "— "+summary)
	}
	return strings.Join(parts, " ")
}

func humanContractTitle(section string) string {
	return map[string]string{
		"before_tool_call": "Before tool calls:",
		"after_tool_call":  "After tool calls:",
		"final_response":   "Final response:",
	}[section]
}
