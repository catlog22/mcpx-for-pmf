package observation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func humanToolOutput(tool string, result map[string]any) string {
	if tool == "context_query" || tool == "source_read" || tool == "file_read" {
		summary := contextQueryOutputSummary(result)
		if tool == "source_read" || tool == "file_read" {
			if format := sourceFormatSummary(result); format != "" {
				if summary == "" {
					return format
				}
				return summary + " · " + format
			}
		}
		if summary != "" {
			return summary
		}
	}
	if data := remoteEnvelopeData(result); data != nil {
		if summary := remoteDataSummary(tool, data); summary != "" {
			return summary
		}
	}
	if structured, _ := result["structured_content"].(map[string]any); structured != nil {
		if summary := structuredToolOutputSummary(tool, structured); summary != "" {
			return summary
		}
	}
	textBlocks := textContentBlocks(result)
	if len(textBlocks) > 0 {
		summaries := make([]string, 0, len(textBlocks))
		for _, text := range textBlocks {
			if summary := toolOutputSummary(text); summary != "" {
				summaries = append(summaries, summary)
			}
		}
		return strings.Join(summaries, " · ")
	}
	structured, _ := result["structured_content"].(map[string]any)
	if structured != nil {
		return compactMap(structured)
	}
	return ""
}

func sourceFormatSummary(result map[string]any) string {
	structured, _ := result["structured_content"].(map[string]any)
	if structured == nil {
		return ""
	}
	if format, ok := structured["format"].(map[string]any); ok {
		return formatMetadataSummary(structured, format)
	}
	items, _ := structured["items"].([]any)
	if len(items) == 0 {
		return ""
	}
	formats := make([]string, 0, 2)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		format, _ := item["format"].(map[string]any)
		if summary := formatMetadataSummary(item, format); summary != "" {
			formats = append(formats, summary)
		}
		if len(formats) == 2 {
			break
		}
	}
	if len(formats) == 0 {
		return ""
	}
	if len(formats) == 1 {
		return formats[0]
	}
	return fmt.Sprintf("formats: %d files", len(items))
}

func formatMetadataSummary(item, format map[string]any) string {
	if len(format) == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	charset, _ := format["charset"].(string)
	if strings.TrimSpace(charset) != "" {
		parts = append(parts, "format="+strings.TrimSpace(charset))
	}
	bom, _ := format["bom"].(string)
	if strings.TrimSpace(bom) != "" && bom != "none" {
		parts = append(parts, "bom="+strings.TrimSpace(bom))
	}
	lineEnding, _ := format["line_ending"].(string)
	if strings.TrimSpace(lineEnding) != "" && lineEnding != "none" {
		parts = append(parts, "line-ending="+strings.TrimSpace(lineEnding))
	}
	if finalNewline, ok := format["final_newline"].(bool); ok {
		if finalNewline {
			parts = append(parts, "final-newline=yes")
		} else {
			parts = append(parts, "final-newline=no")
		}
	}
	if revision, _ := item["sha256"].(string); strings.TrimSpace(revision) != "" {
		parts = append(parts, "sha256="+strings.TrimSpace(revision))
	}
	return strings.Join(parts, " · ")
}

func structuredToolOutputSummary(tool string, data map[string]any) string {
	switch tool {
	case "extension_manage", "extension_discover":
		return extensionManageOutputSummary(data)
	case "runtime_inspect", "runtime_read":
		return runtimeInspectOutputSummary(data)
	}
	return ""
}

func remoteEnvelopeData(result map[string]any) map[string]any {
	rawContent, _ := result["content"].([]any)
	for _, raw := range rawContent {
		item, _ := raw.(map[string]any)
		if item["type"] != "text" {
			continue
		}
		text, _ := item["text"].(string)
		var envelope map[string]any
		if json.Unmarshal([]byte(text), &envelope) != nil {
			continue
		}
		data, _ := envelope["data"].(map[string]any)
		if data != nil {
			return data
		}
	}
	return nil
}

func remoteDataSummary(tool string, data map[string]any) string {
	switch tool {
	case "workspace_list":
		items, _ := data["workspaces"].([]any)
		if len(items) == 0 {
			return "No registered workspaces."
		}
		paths := make([]string, 0, len(items))
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			name, _ := item["name"].(string)
			path, _ := item["path"].(string)
			if name == "" && path == "" {
				continue
			}
			if path != "" {
				paths = append(paths, name+" ("+path+")")
			} else {
				paths = append(paths, name)
			}
		}
		return fmt.Sprintf("Available workspaces: %s", compactPathList(paths))
	case "session_open":
		remote, _ := data["remote_session"].(map[string]any)
		workspace, _ := data["workspace"].(map[string]any)
		id, _ := remote["id"].(string)
		name, _ := workspace["name"].(string)
		if id == "" {
			return ""
		}
		if name == "" {
			name, _ = remote["workspace_name"].(string)
		}
		return fmt.Sprintf("Session %s opened for workspace %s.", id, name)
	case "plan_manage", "plan_create", "plan_read", "plan_transition":
		return planManageOutputSummary(data)
	case "environment_inspect", "environment_read", "environment_snapshot_create":
		return environmentInspectOutputSummary(data)
	case "runtime_inspect", "runtime_read":
		return runtimeInspectOutputSummary(data)
	case "workspace_state", "workspace_observe":
		return workspaceStateOutputSummary(data)
	case "screenshot_capture":
		return screenshotCaptureOutputSummary(data)
	}
	return ""
}

func extensionManageOutputSummary(data map[string]any) string {
	parts := make([]string, 0, 2)
	if skills, exists := data["skills"]; exists {
		names := namedItems(skills, 6)
		if len(names) == 0 {
			parts = append(parts, "Skill：无匹配项")
		} else {
			parts = append(parts, fmt.Sprintf("Skill %d 项：%s", collectionLength(skills), strings.Join(names, "、")))
		}
	}
	if servers, exists := data["upstream_mcp"]; exists {
		names := namedItems(servers, 6)
		if len(names) == 0 {
			parts = append(parts, "MCP：无匹配项")
		} else {
			parts = append(parts, fmt.Sprintf("MCP %d 项：%s", collectionLength(servers), strings.Join(names, "、")))
		}
	}
	if skill, ok := data["skill"].(map[string]any); ok {
		if name, _ := skill["name"].(string); strings.TrimSpace(name) != "" {
			parts = append(parts, "Skill 已描述："+strings.TrimSpace(name))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；") + "。"
}

func namedItems(value any, limit int) []string {
	items, _ := value.([]any)
	if len(items) == 0 || limit <= 0 {
		return nil
	}
	names := make([]string, 0, minInt(len(items), limit))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		name, _ := item["name"].(string)
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
			if len(names) == limit {
				break
			}
		}
	}
	return names
}

func planManageOutputSummary(data map[string]any) string {
	planID, _ := data["plan_id"].(string)
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return ""
	}
	details := make([]string, 0, 3)
	if taskID, _ := data["plan_task_id"].(string); strings.TrimSpace(taskID) != "" {
		details = append(details, "任务 "+strings.TrimSpace(taskID))
	}
	if status, _ := data["status"].(string); strings.TrimSpace(status) != "" {
		details = append(details, "状态 "+strings.TrimSpace(status))
	}
	if tasks, ok := data["tasks"].([]any); ok && len(tasks) > 0 {
		taskSummary := fmt.Sprintf("任务 %d 个", len(tasks))
		if ids := planTaskIDs(tasks, 8); len(ids) > 0 {
			taskSummary += "（" + strings.Join(ids, "、")
			if len(tasks) > len(ids) {
				taskSummary += fmt.Sprintf("、… +%d", len(tasks)-len(ids))
			}
			taskSummary += "）"
		}
		details = append(details, taskSummary)
	} else if progress, ok := data["progress"].(map[string]any); ok {
		if total := formatNumber(progress["total"]); total != "" && total != "0" {
			details = append(details, "任务 "+total+" 个")
		}
	}
	if ready, exists := data["ready"]; exists {
		details = append(details, "可交付 "+formatBool(ready))
	}
	if len(details) == 0 {
		return "Plan " + planID + " updated."
	}
	return "Plan " + planID + "：" + strings.Join(details, "，") + "。"
}

func planTaskIDs(tasks []any, limit int) []string {
	if limit <= 0 {
		return nil
	}
	ids := make([]string, 0, minInt(len(tasks), limit))
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		id, _ := task["plan_task_id"].(string)
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
			if len(ids) == limit {
				break
			}
		}
	}
	return ids
}

func environmentInspectOutputSummary(data map[string]any) string {
	parts := make([]string, 0, 2)
	if snapshotID, _ := data["snapshot_id"].(string); strings.TrimSpace(snapshotID) != "" {
		parts = append(parts, "环境快照 "+strings.TrimSpace(snapshotID)+" 已保存")
	}
	if toolchains := environmentToolchains(data["toolchains"]); len(toolchains) > 0 {
		parts = append(parts, "工具链："+strings.Join(toolchains, "，"))
	}
	if len(parts) == 0 {
		return "环境检查已完成。"
	}
	return strings.Join(parts, "；") + "。"
}

func environmentToolchains(value any) []string {
	toolchains, _ := value.(map[string]any)
	if len(toolchains) == 0 {
		return nil
	}
	preferred := []string{"python", "go", "git", "node", "java"}
	keys := make([]string, 0, len(toolchains))
	seen := make(map[string]bool, len(toolchains))
	for _, key := range preferred {
		if _, exists := toolchains[key]; exists {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	extra := make([]string, 0, len(toolchains)-len(keys))
	for key := range toolchains {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)

	result := make([]string, 0, 3)
	for _, key := range keys {
		if len(result) >= 3 {
			break
		}
		info, _ := toolchains[key].(map[string]any)
		available, _ := info["available"].(bool)
		if !available {
			continue
		}
		version, _ := info["version"].(string)
		version = compactLine(version)
		if version == "" {
			result = append(result, key)
			continue
		}
		result = append(result, key+" "+version)
	}
	return result
}

func runtimeInspectOutputSummary(data map[string]any) string {
	parts := make([]string, 0, 3)
	if stacks := stringValues(data["stacks"], 3); len(stacks) > 0 {
		parts = append(parts, "技术栈："+strings.Join(stacks, "、"))
	}
	if manifests := stringValues(data["manifests"], 3); len(manifests) > 0 {
		parts = append(parts, "清单："+strings.Join(manifests, "、"))
	}
	if status, _ := data["git_status"].(string); strings.TrimSpace(status) != "" {
		parts = append(parts, "Git："+compactLine(status))
	}
	if len(parts) > 0 {
		return "项目摘要：" + strings.Join(parts, "；") + "。"
	}
	if tools := collectionLength(data["tools"]); tools > 0 {
		return fmt.Sprintf("运行时能力已读取：%d 个工具。", tools)
	}
	if documents := instructionCount(data["instructions"]); documents > 0 {
		return fmt.Sprintf("Agent 指令已读取：%d 份文档。", documents)
	}
	return "运行时信息已读取。"
}

func workspaceStateOutputSummary(data map[string]any) string {
	if _, exists := data["items"]; exists {
		returned := collectionLength(data["items"])
		total := formatNumber(data["total"])
		if total == "" {
			total = "0"
		}
		summary := fmt.Sprintf("项目记忆：返回 %d 条，共 %s 条", returned, total)
		if hasMore, _ := data["has_more"].(bool); hasMore {
			summary += "，还有更多"
		}
		return summary + "。"
	}
	if snapshotID, _ := data["snapshot_id"].(string); strings.TrimSpace(snapshotID) != "" {
		files := ""
		if stats, _ := data["stats"].(map[string]any); stats != nil {
			files = formatNumber(stats["files"])
		}
		if files == "" {
			return "文件快照 " + strings.TrimSpace(snapshotID) + " 已创建。"
		}
		return "文件快照 " + strings.TrimSpace(snapshotID) + " 已创建：" + files + " 个文件。"
	}
	if changes, exists := data["changes"]; exists {
		return fmt.Sprintf("文件变更对比完成：%d 项。", collectionLength(changes))
	}
	return ""
}

func screenshotCaptureOutputSummary(data map[string]any) string {
	width := formatNumber(data["output_width"])
	height := formatNumber(data["output_height"])
	format, _ := data["format"].(string)
	display := formatNumber(data["display"])
	parts := make([]string, 0, 3)
	if width != "" && height != "" {
		parts = append(parts, width+"×"+height)
	}
	if strings.TrimSpace(format) != "" {
		parts = append(parts, strings.TrimSpace(format))
	}
	if display != "" {
		parts = append(parts, "显示器 "+display)
	}
	if len(parts) == 0 {
		return "截图已捕获。"
	}
	return "截图已捕获：" + strings.Join(parts, "，") + "。"
}

func stringValues(value any, limit int) []string {
	items, _ := value.([]any)
	if len(items) == 0 {
		return nil
	}
	result := make([]string, 0, minInt(len(items), limit))
	for _, item := range items {
		text, _ := item.(string)
		if text = strings.TrimSpace(text); text != "" {
			result = append(result, text)
			if len(result) == limit {
				break
			}
		}
	}
	return result
}

func collectionLength(value any) int {
	if items, ok := value.([]any); ok {
		return len(items)
	}
	return 0
}

func instructionCount(value any) int {
	instructions, _ := value.(map[string]any)
	return collectionLength(instructions["documents"])
}

func formatBool(value any) string {
	if value, ok := value.(bool); ok {
		if value {
			return "是"
		}
		return "否"
	}
	return formatNumber(value)
}

func contextQueryOutputSummary(result map[string]any) string {
	structured, _ := result["structured_content"].(map[string]any)
	if structured == nil {
		return ""
	}
	summary := ""
	if blocks := textContentBlocks(result); len(blocks) > 0 {
		summary = blocks[0]
	}
	paths := make([]string, 0)
	if matches, ok := structured["matches"].([]any); ok {
		for _, raw := range matches {
			item, _ := raw.(map[string]any)
			path, _ := item["path"].(string)
			if strings.TrimSpace(path) == "" {
				continue
			}
			if line := formatNumber(item["line"]); line != "" && line != "0" {
				path += ":" + line
			}
			paths = append(paths, path)
		}
	}
	if files, ok := structured["files"].([]any); ok && len(paths) == 0 {
		for _, raw := range files {
			item, _ := raw.(map[string]any)
			path, _ := item["path"].(string)
			if strings.TrimSpace(path) != "" {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		return summary
	}
	if summary == "" {
		summary = fmt.Sprintf("Found %d result(s)", len(paths))
	}
	summary = strings.TrimSuffix(strings.TrimSpace(summary), ".")
	summary = compactLine(summary)
	if len(paths) > 20 {
		summary += " (first 20 shown)"
	}
	return summary + ": " + compactPathList(paths)
}

func compactPathList(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	maxShown := 20 // reasonable limit for terminal + folding
	if len(paths) <= maxShown {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:maxShown], ", ") + fmt.Sprintf(", ... +%d more", len(paths)-maxShown)
}

func toolOutputSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, marker := range []string{"\n### ", "\n```", "\n> "} {
		if index := strings.Index(text, marker); index >= 0 {
			text = text[:index]
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "# ")
		runes := []rune(line)
		if len(runes) > maxToolSummaryRunes {
			return string(runes[:maxToolSummaryRunes-3]) + "..."
		}
		return line
	}
	return ""
}

func summaryLines(text string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	lines := make([]string, 0, maxLines)
	for _, raw := range strings.Split(strings.TrimSpace(text), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if parsed := humanText(line); parsed != line {
			line = parsed
		}
		lines = append(lines, compactLine(line))
		if len(lines) == maxLines {
			break
		}
	}
	return lines
}

func textContentBlocks(result map[string]any) []string {
	var textBlocks []string
	rawContent, ok := result["content"].([]any)
	if !ok {
		return nil
	}
	for _, raw := range rawContent {
		item, _ := raw.(map[string]any)
		if item["type"] != "text" {
			continue
		}
		text, _ := item["text"].(string)
		if text = humanText(text); text != "" {
			textBlocks = append(textBlocks, text)
		}
	}
	return textBlocks
}

func humanText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return text
	}
	if object, ok := value.(map[string]any); ok {
		if summary := errorEnvelopeSummary(object); summary != "" {
			return summary
		}
		return compactMap(object)
	}
	return fmt.Sprint(value)
}

var humanProtocolFields = map[string]struct{}{
	"completed_at":       {},
	"completed_at_ms":    {},
	"network_latency_ms": {},
	"processing_ms":      {},
	"received_at":        {},
	"received_at_ms":     {},
	"remote_session_id":  {},
	"request_id":         {},
	"server_elapsed_ms":  {},
	"started_at":         {},
	"started_at_ms":      {},
	"status":             {},
	"timing":             {},
	"ok":                 {},
}

func compactMap(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, diagnostic := humanProtocolFields[key]; diagnostic {
			continue
		}
		if formatted := compactValue(value[key]); formatted != "" {
			parts = append(parts, key+"="+formatted)
		}
	}
	return strings.Join(parts, " ")
}

func compactValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		line := strings.TrimSpace(strings.SplitN(typed, "\n", 2)[0])
		if line == "" {
			return ""
		}
		return strconv.Quote(line)
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return formatNumber(typed)
	case []any:
		return fmt.Sprintf("%d items", len(typed))
	case []map[string]any:
		return fmt.Sprintf("%d items", len(typed))
	case map[string]any:
		return "object"
	default:
		return fmt.Sprint(typed)
	}
}

func formatNumber(value any) string {
	switch number := value.(type) {
	case float64:
		return fmt.Sprintf("%.0f", number)
	case int:
		return fmt.Sprintf("%d", number)
	case int64:
		return fmt.Sprintf("%d", number)
	default:
		return ""
	}
}

func paint(value, code string, enabled bool) string {
	if !enabled {
		return value
	}
	return code + value + ansiReset
}
