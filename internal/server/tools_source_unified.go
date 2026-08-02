package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/instruction"
	"mcpx/internal/security"
	"mcpx/internal/source"
)

func nextAction(tool string, arguments map[string]any) map[string]any {
	return nextActionWithReason(tool, "continue with the returned operation", arguments)
}

// toolFileReadUnified is the sole public source-read entry. A single path
// retains the concise shape, while items[] runs the same bounded batch reader
// and preserves per-item failures rather than failing the whole call.
func (r *Runtime) toolFileReadUnified(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	if raw, ok := envReq.Payload["items"].([]any); ok && len(raw) > 0 {
		if len(raw) > 20 {
			return r.sourceError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("items exceeds maximum of 20"))
		}
		items := make([]source.BatchReadRequest, 0, len(raw))
		for _, value := range raw {
			item, ok := value.(map[string]any)
			if !ok {
				return r.sourceError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("items must contain objects"))
			}
			path, _ := item["path"].(string)
			if strings.TrimSpace(path) == "" {
				return r.sourceError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("item path is required"))
			}
			items = append(items, source.BatchReadRequest{Path: path, Offset: intPayload(item, "offset"), Limit: intPayload(item, "limit")})
		}
		effective := r.effectiveConfig(session.WorkspacePath)
		budget := intPayload(envReq.Payload, "max_total_bytes")
		if budget <= 0 {
			budget = config.MaxResultBytes(effective.Limits)
		}
		batch := source.ReadBatch(session.WorkspacePath, items, effective.Security.Files.MaxReadBytes, budget, r.sourcePathAllowed(session.WorkspacePath))
		results := make([]map[string]any, 0, len(batch.Results))
		for _, item := range batch.Results {
			entry := map[string]any{
				"path": item.Path, "ok": item.OK, "content": item.Content, "sha256": item.SHA256,
				"offset": item.Offset, "limit": item.Limit, "total_lines": item.TotalLines, "truncated": item.Truncated,
			}
			if item.Truncated {
				nextOffset := item.Offset + strings.Count(item.Content, "\n")
				if nextOffset == item.Offset {
					nextOffset++
				}
				entry["next_offset"] = nextOffset
			}
			if item.Error != "" {
				code, category := "READ_FAILED", "runtime"
				if strings.Contains(item.Error, "denied") {
					code, category = "FILE_DENIED", "security"
				} else if strings.Contains(strings.ToLower(item.Error), "not exist") || strings.Contains(strings.ToLower(item.Error), "no such file") || strings.Contains(strings.ToLower(item.Error), "not found") {
					code, category = "NOT_FOUND", "not_found"
				} else if strings.Contains(item.Error, "budget") {
					code, category = "RESULT_BUDGET_EXCEEDED", "validation"
				}
				details := map[string]any{}
				if code == "NOT_FOUND" {
					details["next_action"] = nextActionWithReason("context_query", "locate this path before retrying file_read", map[string]any{
						"remote_session_id": session.ID,
						"action":            "list",
					})
				}
				entry["error"] = map[string]any{"code": code, "message": item.Error, "category": category, "retryable": code == "RESULT_BUDGET_EXCEEDED", "details": details}
			}
			results = append(results, entry)
		}
		data := map[string]any{
			"results": results, "total_bytes": batch.TotalBytes, "budget_bytes": batch.BudgetBytes, "truncated": batch.Truncated,
		}
		if batch.Truncated {
			data["continue_from"] = batch.ContinueFrom
			items := make([]map[string]any, 0, len(batch.ContinueRequests))
			for _, item := range batch.ContinueRequests {
				items = append(items, map[string]any{"path": item.Path, "offset": item.Offset, "limit": item.Limit})
			}
			data["next_action"] = nextAction("file_read", map[string]any{
				"remote_session_id": session.ID, "items": items, "max_total_bytes": budget,
			})
		}
		summary := fmt.Sprintf("Read %d source item(s); %d bytes returned.", len(results), batch.TotalBytes)
		return mcp.NewToolResultStructured(data, sourceReadDisplay(data, summary)), nil
	}

	path, _ := envReq.Payload["path"].(string)
	if path == "" {
		return r.sourceError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("path or items is required"))
	}
	if security.MatchFile(r.effectiveConfig(session.WorkspacePath).Security.Files, path) != security.Allow {
		response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName, map[string]any{"path": path}, "FILE_DENIED", "file denied by policy")
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	read, err := source.Read(session.WorkspacePath, path, intPayload(envReq.Payload, "offset"), intPayload(envReq.Payload, "limit"), r.effectiveConfig(session.WorkspacePath).Security.Files.MaxReadBytes)
	if err != nil {
		return r.sourceError(envReq, session.ID, session.WorkspaceName, err)
	}
	data := map[string]any{
		"path": read.Path, "content": read.Content, "sha256": read.SHA256, "offset": read.Offset, "limit": read.Limit, "total_lines": read.TotalLines, "truncated": read.Truncated,
	}
	if read.Truncated {
		data["next_action"] = nextAction("file_read", map[string]any{"remote_session_id": session.ID, "path": path, "offset": read.Offset + read.Limit, "limit": read.Limit})
	}
	summary := fmt.Sprintf("Read %s (%d lines).", path, read.TotalLines)
	return mcp.NewToolResultStructured(data, sourceReadDisplay(data, summary)), nil
}

// sourceReadDisplay is the host/model-facing representation of file_read.
// The public ARC wrapper keeps the complete machine data in response metadata;
// the first text content remains useful to a terminal agent without requiring
// it to decode a protocol envelope before it can inspect source code.
func sourceReadDisplay(data map[string]any, summary string) string {
	var builder strings.Builder
	if summary != "" {
		builder.WriteString(summary)
	}
	for _, item := range sourceReadItems(data) {
		path, _ := item["path"].(string)
		content, _ := item["content"].(string)
		if path == "" || content == "" {
			if errValue, ok := item["error"].(map[string]any); ok {
				if message, _ := errValue["message"].(string); message != "" {
					fmt.Fprintf(&builder, "\n\n`%s`: %s", path, message)
				}
			}
			continue
		}

		start := sourceReadNumber(item["offset"]) + 1
		shownLines := strings.Count(content, "\n")
		if shownLines == 0 {
			shownLines = 1
		}
		end := start + shownLines - 1
		lineLabel := fmt.Sprintf("lines %d-%d", start, end)
		if total := sourceReadNumber(item["total_lines"]); total > 0 {
			lineLabel += fmt.Sprintf(" of %d", total)
		}

		fmt.Fprintf(&builder, "\n\n### `%s` (%s)\n\n```%s\n", path, lineLabel, sourceReadLanguage(path))
		builder.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			builder.WriteByte('\n')
		}
		builder.WriteString("```")
		if truncated, _ := item["truncated"].(bool); truncated {
			builder.WriteString("\n\n> 内容已截断；请继续调用 `file_read` 读取后续内容。")
		}
	}
	return builder.String()
}

func sourceReadItems(data map[string]any) []map[string]any {
	if raw, ok := data["results"]; ok {
		switch items := raw.(type) {
		case []map[string]any:
			return items
		case []any:
			result := make([]map[string]any, 0, len(items))
			for _, rawItem := range items {
				if item, ok := rawItem.(map[string]any); ok {
					result = append(result, item)
				}
			}
			return result
		}
	}
	return []map[string]any{data}
}

func sourceReadNumber(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	default:
		return 0
	}
}

func sourceReadLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".vue":
		return "vue"
	case ".java":
		return "java"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md":
		return "markdown"
	case ".css", ".scss":
		return "css"
	case ".html", ".htm":
		return "html"
	default:
		return "text"
	}
}

func (r *Runtime) toolContextQueryUnified(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	if action == "query" {
		return r.toolContextQueryAction(ctx, req)
	}
	if action == "search" {
		return r.toolContextSearchAction(ctx, req)
	}
	if action == "list" {
		return r.toolContextListAction(ctx, req)
	}
	return r.invalidAction(ctx, req, "context_query", action)
}

func (r *Runtime) toolContextQueryAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	query, _ := envReq.Payload["query"].(string)
	if strings.TrimSpace(query) == "" {
		return r.sourceError(envReq, session.ID, session.WorkspaceName, fmt.Errorf("query is required"))
	}
	mode := sourcePayloadString(envReq.Payload, "mode")
	if mode == "" {
		mode = "smart"
	}
	parallel := true
	if _, exists := envReq.Payload["parallel"]; exists {
		parallel = boolPayload(envReq.Payload, "parallel")
	}
	maxResults := intPayload(envReq.Payload, "max_results")
	var seeds []string
	if raw, ok := envReq.Payload["paths"].([]any); ok {
		for _, value := range raw {
			if path, ok := value.(string); ok && path != "" {
				seeds = append(seeds, path)
			}
		}
	}
	include, _ := envReq.Payload["include_glob"].(string)
	exclude, _ := envReq.Payload["exclude_glob"].(string)
	allowed := r.sourcePathAllowedWithGlobs(session.WorkspacePath, include, exclude)
	maxBytes := r.effectiveConfig(session.WorkspacePath).Security.Files.MaxReadBytes
	if requested := intPayload(envReq.Payload, "max_bytes_per_file"); requested > 0 && int64(requested) < maxBytes {
		maxBytes = int64(requested)
	}
	data, err := source.SmartQueryPage(session.WorkspacePath, source.SmartQueryOptions{
		Query: query, Mode: mode, Parallel: parallel, MaxResults: maxResults,
		Cursor: sourcePayloadString(envReq.Payload, "cursor"), Pattern: include, ExcludePattern: exclude,
		ContextBefore: intPayload(envReq.Payload, "context_before"), ContextAfter: intPayload(envReq.Payload, "context_after"),
		MaxBytesPerFile: maxBytes, IncludeSHA256: boolPayload(envReq.Payload, "include_sha256"), Allowed: allowed,
	})
	if err != nil {
		return r.sourceError(envReq, session.ID, session.WorkspaceName, err)
	}
	if include, _ := envReq.Payload["include_instructions"].(bool); include {
		anchor := ""
		if len(seeds) > 0 {
			anchor = seeds[0]
		}
		docs := instruction.DiscoverAt(r.cfg.Discovery.Instructions.GlobalAgentsPath, session.WorkspacePath, anchor, r.effectiveConfig(session.WorkspacePath).Security.Files.MaxReadBytes)
		data["instructions"] = docs
	}
	if truncated, _ := data["truncated"].(bool); truncated {
		data["next_action"] = nextAction("context_query", map[string]any{
			"remote_session_id": session.ID, "action": "query", "query": query, "paths": seeds,
			"mode": mode, "parallel": parallel, "max_results": maxResults, "cursor": data["next_cursor"],
			"max_bytes_per_file": intPayload(envReq.Payload, "max_bytes_per_file"),
			"include_glob":       sourcePayloadString(envReq.Payload, "include_glob"),
			"exclude_glob":       sourcePayloadString(envReq.Payload, "exclude_glob"),
		})
	}
	files, _ := data["files"].([]map[string]any)
	return compactToolResult(data, fmt.Sprintf("Context query returned %d file(s).", len(files))), nil
}

func (r *Runtime) toolContextSearchAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	query, _ := envReq.Payload["query"].(string)
	pattern, _ := envReq.Payload["include_glob"].(string)
	regex, _ := envReq.Payload["regex"].(bool)
	caseSensitive, setCase := envReq.Payload["case_sensitive"].(bool)
	if !setCase {
		caseSensitive = true // retain existing source search behaviour by default.
	}
	resultData, err := source.SearchWith(session.WorkspacePath, source.SearchOptions{
		Query: query, Pattern: pattern, ExcludePattern: sourcePayloadString(envReq.Payload, "exclude_glob"), Cursor: sourcePayloadString(envReq.Payload, "cursor"), Regex: regex,
		CaseSensitive: caseSensitive, Limit: intPayload(envReq.Payload, "limit"), ContextBefore: intPayload(envReq.Payload, "context_before"), ContextAfter: intPayload(envReq.Payload, "context_after"), IncludeSHA256: boolPayload(envReq.Payload, "include_sha256"),
	}, r.sourcePathAllowed(session.WorkspacePath))
	if err != nil {
		return r.sourceError(envReq, session.ID, session.WorkspaceName, err)
	}
	data := map[string]any{"matches": resultData.Matches, "truncated": resultData.Truncated}
	if resultData.NextCursor != "" {
		data["next_cursor"] = resultData.NextCursor
		data["next_action"] = nextAction("context_query", map[string]any{
			"remote_session_id": session.ID, "action": "search", "query": query,
			"cursor": resultData.NextCursor, "limit": intPayload(envReq.Payload, "limit"),
			"include_glob": pattern, "exclude_glob": sourcePayloadString(envReq.Payload, "exclude_glob"),
			"regex": regex, "case_sensitive": caseSensitive,
			"include_sha256": boolPayload(envReq.Payload, "include_sha256"),
			"context_before": intPayload(envReq.Payload, "context_before"),
			"context_after":  intPayload(envReq.Payload, "context_after"),
		})
	}
	return compactToolResult(data, fmt.Sprintf("Source search returned %d match(es).", len(resultData.Matches))), nil
}

func (r *Runtime) toolContextListAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	pattern := sourcePayloadString(envReq.Payload, "include_glob")
	list, err := source.ListWith(session.WorkspacePath, pattern, sourcePayloadString(envReq.Payload, "exclude_glob"), sourcePayloadString(envReq.Payload, "cursor"), intPayload(envReq.Payload, "limit"), boolPayload(envReq.Payload, "include_sha256"), r.sourcePathAllowed(session.WorkspacePath))
	if err != nil {
		return r.sourceError(envReq, session.ID, session.WorkspaceName, err)
	}
	data := map[string]any{"files": list.Files, "total": list.Total}
	if list.NextCursor != "" {
		data["next_cursor"] = list.NextCursor
		data["next_action"] = nextAction("context_query", map[string]any{
			"remote_session_id": session.ID, "action": "list", "include_glob": pattern,
			"exclude_glob": sourcePayloadString(envReq.Payload, "exclude_glob"), "cursor": list.NextCursor,
			"limit": intPayload(envReq.Payload, "limit"), "include_sha256": boolPayload(envReq.Payload, "include_sha256"),
		})
	}
	return compactToolResult(data, fmt.Sprintf("Source list returned %d of %d file(s).", len(list.Files), list.Total)), nil
}

func (r *Runtime) sourcePathAllowedWithGlobs(workspacePath, include, exclude string) func(string) bool {
	base := r.sourcePathAllowed(workspacePath)
	include = strings.TrimSpace(include)
	exclude = strings.TrimSpace(exclude)
	return func(path string) bool {
		if !base(path) {
			return false
		}
		if include != "" {
			matched, err := source.MatchGlob(include, path)
			if err != nil || !matched {
				return false
			}
		}
		if exclude != "" {
			matched, err := source.MatchGlob(exclude, path)
			if err == nil && matched {
				return false
			}
		}
		return true
	}
}

func sourcePayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func boolPayload(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}
