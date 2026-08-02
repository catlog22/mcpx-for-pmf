// Package arc implements the MCPX Agent Result Contract.
package arc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

const Version = "1.2"

// ResultMetadataKey identifies the hidden response metadata that carries the
// complete ARC envelope. Keeping the envelope in _meta prevents MCP hosts
// from rendering it as a large JSON result card while preserving a machine-
// readable copy for clients that explicitly need it.
const ResultMetadataKey = "mcpx.result"

const (
	SchemaText              = "mcpx.text.v1"
	SchemaMarkdown          = "mcpx.markdown.v1"
	SchemaSearchResult      = "mcpx.search_result.v1"
	SchemaCodeChange        = "mcpx.code_change.v1"
	SchemaTable             = "mcpx.table.v1"
	SchemaFileTree          = "mcpx.file_tree.v1"
	SchemaLog               = "mcpx.log.v1"
	SchemaError             = "mcpx.error.v1"
	SchemaDiagram           = "mcpx.diagram.v1"
	SchemaDiagramCollection = "mcpx.diagram_collection.v1"
	SchemaPlan              = "mcpx.plan.v1"
	SchemaPlanTask          = "mcpx.plan_task.v1"
	SchemaDelivery          = "mcpx.delivery.v1"
)

type Timing struct {
	StartedAtMs      int64
	ReceivedAtMs     int64
	CompletedAtMs    int64
	NetworkLatencyMs int64
	ProcessingMs     int64
	ServerElapsedMs  int64
}

type ResultContext struct {
	RequestID string
	TraceID   string
	SpanID    string
	Timing    Timing
}

type Trace struct {
	TraceID          string `json:"trace_id"`
	SpanID           string `json:"span_id,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	Source           string `json:"source"`
	Tool             string `json:"tool"`
	StartedAtMs      int64  `json:"started_at_ms"`
	ReceivedAtMs     int64  `json:"received_at_ms"`
	CompletedAtMs    int64  `json:"completed_at_ms"`
	NetworkLatencyMs int64  `json:"network_latency_ms"`
	Duration         struct {
		ServerMs int64 `json:"server_ms"`
	} `json:"duration"`
}

type Hints struct {
	PreferredBehavior string `json:"preferred_behavior,omitempty"`
}

type Action struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Label     string         `json:"label"`
	Confirm   bool           `json:"confirm"`
	Arguments map[string]any `json:"arguments"`
}

type Result struct {
	Type         string        `json:"type"`
	Schema       string        `json:"schema"`
	Summary      string        `json:"summary,omitempty"`
	Data         any           `json:"data"`
	Presentation *Presentation `json:"presentation,omitempty"`
	Hints        Hints         `json:"hints,omitempty"`
	Actions      []Action      `json:"actions,omitempty"`
}

type Envelope struct {
	MCPX struct {
		Version string `json:"version"`
		Trace   Trace  `json:"trace"`
		Result  Result `json:"result"`
	} `json:"mcpx"`
}

// WrapToolResult converts an internal handler result to the public MCPX result
// format. The first text content is the host-visible rendered display (source
// blocks, Markdown diffs, or a concise summary). The complete ARC envelope is
// kept in _meta[ResultMetadataKey] instead of structuredContent: many MCP
// hosts render structuredContent as a raw JSON card, which hides the useful
// Markdown result from both the model and the user. Non-text protocol content
// such as images and resource links is preserved.
func WrapToolResult(tool string, runtime ResultContext, raw *mcp.CallToolResult) *mcp.CallToolResult {
	if raw == nil {
		raw = mcp.NewToolResultError("tool returned no result")
	}
	data, summary := extractResult(raw)
	resultType, resultData, hints, actions := classify(tool, raw.IsError, data, summary)
	if summary == "" {
		summary = fmt.Sprintf("%s result returned.", tool)
	}
	renderer := "text"
	if resultType == "code_change" {
		renderer = "diff"
	}
	display, _ := RenderToolContent(tool, resultType, renderer, summary, resultData)

	var envelope Envelope
	envelope.MCPX.Version = Version
	envelope.MCPX.Trace = buildTrace(tool, runtime, runtime.Timing)
	envelope.MCPX.Result = Result{
		Type: resultType, Schema: schemaForType(resultType), Summary: summary,
		Data: resultData, Presentation: DefaultPresentation(resultType), Hints: hints, Actions: actions,
	}
	content := []mcp.Content{mcp.NewTextContent(display)}
	for _, item := range raw.Content {
		if _, isText := item.(mcp.TextContent); !isText {
			content = append(content, item)
		}
	}
	raw.Content = content
	// The human-readable content is the default presentation. Preserve the
	// machine-readable ARC envelope in response metadata without triggering
	// host UIs that display structuredContent verbatim as JSON.
	raw.StructuredContent = nil
	raw.RawStructuredContent = nil
	setMetadata(raw, envelope, resultType)
	return raw
}

func buildTrace(tool string, runtime ResultContext, timing Timing) Trace {
	traceID := runtime.TraceID
	if traceID == "" {
		traceID = newTraceID()
	}
	spanID := runtime.SpanID
	if spanID == "" {
		spanID = newTraceID()
	}
	trace := Trace{
		TraceID: traceID, SpanID: spanID, RequestID: runtime.RequestID, Source: "mcpx", Tool: tool,
		StartedAtMs: timing.StartedAtMs, ReceivedAtMs: timing.ReceivedAtMs,
		CompletedAtMs: timing.CompletedAtMs, NetworkLatencyMs: timing.NetworkLatencyMs,
	}
	trace.Duration.ServerMs = timing.ServerElapsedMs
	return trace
}

func setMetadata(result *mcp.CallToolResult, envelope Envelope, resultType string) {
	trace := envelope.MCPX.Trace
	if result.Meta == nil {
		result.Meta = &mcp.Meta{AdditionalFields: map[string]any{}}
	}
	if result.Meta.AdditionalFields == nil {
		result.Meta.AdditionalFields = map[string]any{}
	}
	result.Meta.AdditionalFields["mcpx.version"] = Version
	result.Meta.AdditionalFields["mcpx.trace_id"] = trace.TraceID
	result.Meta.AdditionalFields["mcpx.span_id"] = trace.SpanID
	result.Meta.AdditionalFields["mcpx.request_id"] = trace.RequestID
	result.Meta.AdditionalFields["mcpx.result_type"] = resultType
	result.Meta.AdditionalFields["mcpx.processing_ms"] = trace.Duration.ServerMs - trace.NetworkLatencyMs
	result.Meta.AdditionalFields["mcpx.server_elapsed_ms"] = trace.Duration.ServerMs
	result.Meta.AdditionalFields[ResultMetadataKey] = envelope
}

func extractResult(raw *mcp.CallToolResult) (map[string]any, string) {
	if data, ok := toMap(raw.StructuredContent); ok {
		return data, resultSummary(data, firstText(raw))
	}
	text := firstText(raw)
	if text == "" {
		return nil, ""
	}
	var data map[string]any
	if json.Unmarshal([]byte(text), &data) == nil {
		return data, resultSummary(data, text)
	}
	return map[string]any{"text": text}, text
}

func classify(tool string, isError bool, data map[string]any, summary string) (string, any, Hints, []Action) {
	if data == nil {
		return "error", map[string]any{"message": summary}, Hints{PreferredBehavior: "summarize"}, nil
	}
	if isError || isErrorData(data) {
		behavior := "summarize"
		status, _ := data["status"].(string)
		if status == "need_confirmation" || status == "need_secret" {
			behavior = "ask_confirm"
		}
		errorResult := errorData(data)
		if hasAnyKey(errorResult, "changeset_id") && hasAnyKey(errorResult, "files", "diff") {
			return "code_change", errorResult, Hints{PreferredBehavior: behavior}, actionsFrom(data)
		}
		return "error", errorResult, Hints{PreferredBehavior: behavior}, actionsFrom(data)
	}

	inner := nestedData(data)
	if diagramType, diagramData, ok := detectDiagramResult(tool, inner, summary); ok {
		return diagramType, diagramData, Hints{PreferredBehavior: "show_directly"}, actionsFrom(data)
	}
	resultType := "text"
	switch {
	case tool == "plan_manage" && hasAnyKey(inner, "ready", "checks", "blockers"):
		resultType = "delivery"
	case tool == "plan_manage" && hasAnyKey(inner, "task_id", "task"):
		resultType = "plan_task"
	case tool == "plan_manage":
		resultType = "plan"
	case tool == "change_execute" || tool == "change_manage":
		// Tool identity wins over content heuristics: the Changeset DTO also
		// carries a "files" key which would otherwise classify as search_result.
		resultType = "code_change"
	case tool == "context_query":
		resultType = "search_result"
	case hasAnyKey(inner, "files", "matches"):
		resultType = "search_result"
	case hasAnyKey(inner, "changeset_id", "diff"):
		resultType = "code_change"
	case tool == "command_execute" || tool == "task_manage" || hasAnyKey(inner, "stdout", "stderr", "exit_code"):
		resultType = "log"
	case hasAnyKey(inner, "columns", "rows"):
		resultType = "table"
	case hasAnyKey(inner, "tree", "entries"):
		resultType = "file_tree"
	}
	behavior := "show_directly"
	if completed, ok := inner["completed_in_call"].(bool); ok && !completed {
		behavior = "continue"
	}
	if data["status"] == "need_confirmation" {
		behavior = "ask_confirm"
	}
	return resultType, inner, Hints{PreferredBehavior: behavior}, actionsFrom(data)
}

func isErrorData(data map[string]any) bool {
	status, _ := data["status"].(string)
	return data["error"] != nil || status == "error" || status == "denied" || status == "unauthorized" || status == "need_confirmation" || status == "need_secret"
}

func errorData(data map[string]any) map[string]any {
	result := map[string]any{}
	if inner, ok := data["data"].(map[string]any); ok {
		for key, value := range inner {
			result[key] = value
		}
	}
	if value, ok := data["error"]; ok && value != nil {
		result["error"] = value
	}
	if status, ok := data["status"].(string); ok && status != "" {
		result["status"] = status
	}
	if len(result) == 0 {
		for key, value := range data {
			result[key] = value
		}
	}
	return result
}

func nestedData(data map[string]any) map[string]any {
	if inner, ok := data["data"].(map[string]any); ok {
		return inner
	}
	return data
}

func detectDiagramResult(tool string, inner map[string]any, summary string) (string, map[string]any, bool) {
	if tool != "context_query" || boolValue(inner, "truncated") {
		return "", nil, false
	}
	for _, candidate := range []string{stringValue(inner, "markdown"), stringValue(inner, "content"), stringValue(inner, "text"), summary} {
		blocks := extractMermaidBlocks(strings.TrimSpace(candidate))
		if len(blocks) == 0 {
			continue
		}
		if len(blocks) == 1 {
			result := map[string]any{"source": blocks[0], "mermaid": blocks[0]}
			if candidate != summary {
				result["markdown"] = candidate
			}
			return "diagram", result, true
		}
		diagrams := make([]map[string]any, 0, len(blocks))
		for index, source := range blocks {
			diagrams = append(diagrams, map[string]any{"id": fmt.Sprintf("diagram_%d", index+1), "source": source, "mermaid": source})
		}
		result := map[string]any{"diagrams": diagrams}
		if candidate != summary {
			result["markdown"] = candidate
		}
		return "diagram_collection", result, true
	}
	return "", nil, false
}

func stringValue(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

func boolValue(data map[string]any, key string) bool {
	value, _ := data[key].(bool)
	return value
}

func actionsFrom(data map[string]any) []Action {
	next, ok := data["next_action"].(map[string]any)
	if !ok {
		if inner, nestedOK := data["data"].(map[string]any); nestedOK {
			next, ok = inner["next_action"].(map[string]any)
		}
	}
	if !ok {
		if rawError, errorOK := data["error"].(map[string]any); errorOK {
			if details, detailsOK := rawError["details"].(map[string]any); detailsOK {
				next, ok = details["next_action"].(map[string]any)
				if !ok {
					if actions, actionsOK := details["next_actions"].([]any); actionsOK && len(actions) > 0 {
						next, ok = actions[0].(map[string]any)
					}
				}
			}
		}
	}
	if !ok {
		return nil
	}
	tool, _ := next["tool"].(string)
	args, _ := next["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	if tool == "" {
		return nil
	}
	actionType := "continue"
	confirm := false
	label := "Continue with " + tool
	if strings.Contains(tool, "change") {
		actionType, confirm, label = "mutation", true, "Continue with "+tool
	}
	return []Action{{ID: tool, Type: actionType, Label: label, Confirm: confirm, Arguments: args}}
}

func hasAnyKey(data map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := data[key]; ok {
			return true
		}
	}
	return false
}

func resultSummary(data map[string]any, fallback string) string {
	if summary, ok := data["summary"].(string); ok && strings.TrimSpace(summary) != "" {
		return summary
	}
	if message, ok := data["message"].(string); ok && message != "" {
		return message
	}
	if errData, ok := data["error"].(map[string]any); ok {
		if message, ok := errData["message"].(string); ok && message != "" {
			return message
		}
	}
	status, _ := data["status"].(string)
	if status == "error" || status == "denied" || status == "unauthorized" || status == "need_confirmation" || status == "need_secret" {
		return strings.ReplaceAll(status, "_", " ")
	}
	if strings.TrimSpace(fallback) != "" {
		var encoded any
		if json.Unmarshal([]byte(fallback), &encoded) == nil {
			// Envelope.Response and legacy structured payloads must never leak
			// into host-visible text as raw JSON. RenderToolContent receives the
			// decoded data and will provide the useful Markdown representation.
			if status != "" && status != "ok" {
				return strings.ReplaceAll(status, "_", " ")
			}
			return "ok"
		}
		return fallback
	}
	if status != "" && status != "ok" {
		return strings.ReplaceAll(status, "_", " ")
	}
	return ""
}

func toMap(value any) (map[string]any, bool) {
	if data, ok := value.(map[string]any); ok {
		return data, true
	}
	if value == nil {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var data map[string]any
	if json.Unmarshal(encoded, &data) != nil {
		return nil, false
	}
	return data, true
}

func firstText(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func newTraceID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "tr_" + time.Now().UTC().Format("20060102") + "_" + hex.EncodeToString(raw[:])
}

func schemaForType(resultType string) string {
	switch resultType {
	case "markdown":
		return SchemaMarkdown
	case "search_result":
		return SchemaSearchResult
	case "code_change":
		return SchemaCodeChange
	case "table":
		return SchemaTable
	case "file_tree":
		return SchemaFileTree
	case "log":
		return SchemaLog
	case "error":
		return SchemaError
	case "diagram":
		return SchemaDiagram
	case "diagram_collection":
		return SchemaDiagramCollection
	case "plan":
		return SchemaPlan
	case "plan_task":
		return SchemaPlanTask
	case "delivery":
		return SchemaDelivery
	default:
		return SchemaText
	}
}

var publicSchemaNames = []string{
	SchemaText, SchemaMarkdown, SchemaSearchResult, SchemaCodeChange, SchemaTable,
	SchemaFileTree, SchemaLog, SchemaError, SchemaDiagram, SchemaDiagramCollection,
	SchemaPlan, SchemaPlanTask, SchemaDelivery,
}

var schemaRegistryCache struct {
	sync.Once
	value map[string]json.RawMessage
}

// SchemaRegistry returns a copy of the public ARC result schemas.
func SchemaRegistry() map[string]json.RawMessage {
	schemaRegistryCache.Do(func() {
		registry := make(map[string]json.RawMessage, len(publicSchemaNames))
		for _, name := range publicSchemaNames {
			registry[name] = resultSchema(name)
		}
		schemaRegistryCache.value = registry
	})
	registry := make(map[string]json.RawMessage, len(schemaRegistryCache.value))
	for name, schema := range schemaRegistryCache.value {
		registry[name] = append(json.RawMessage(nil), schema...)
	}
	return registry
}

func resultSchema(name string) json.RawMessage {
	schema := map[string]any{
		"$id": name, "type": "object", "additionalProperties": true,
		"properties": map[string]any{"data": resultDataSchema(name)},
	}
	encoded, _ := json.Marshal(schema)
	return encoded
}

func resultDataSchema(name string) map[string]any {
	object := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "additionalProperties": true}
	}
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	switch name {
	case SchemaSearchResult:
		return object(map[string]any{
			"query": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"},
			"analysis": map[string]any{"type": "object"}, "files": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"matches": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "total_bytes": map[string]any{"type": "integer"},
			"truncated": map[string]any{"type": "boolean"}, "next_cursor": map[string]any{"type": "string"},
		})
	case SchemaCodeChange:
		return object(map[string]any{
			"changeset_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"},
			"summary": map[string]any{"type": "string"}, "digest": map[string]any{"type": "string"},
			"files": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "new_path": map[string]any{"type": "string"},
				"operation": map[string]any{"type": "string"}, "diff": map[string]any{"type": "string"},
				"diff_truncated": map[string]any{"type": "boolean"},
			}}}, "diff": map[string]any{"type": "object"},
			"applied": map[string]any{"type": "boolean"}, "verify": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})
	case SchemaTable:
		return object(map[string]any{
			"columns": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"rows":    map[string]any{"type": "array", "items": map[string]any{}},
		})
	case SchemaFileTree:
		return object(map[string]any{"tree": map[string]any{"type": "object"}, "entries": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}})
	case SchemaLog:
		return object(map[string]any{
			"task_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"},
			"stdout": map[string]any{"type": "string"}, "stderr": map[string]any{"type": "string"},
			"stdout_next_offset": map[string]any{"type": "integer"}, "stderr_next_offset": map[string]any{"type": "integer"},
		})
	case SchemaError:
		return object(map[string]any{
			"status": map[string]any{"type": "string"}, "error": map[string]any{"type": "object"},
			"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
		})
	case SchemaText:
		return object(map[string]any{"text": map[string]any{"type": "string"}})
	case SchemaMarkdown:
		return object(map[string]any{
			"markdown": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"},
		})
	case SchemaDiagram:
		return object(map[string]any{
			"source": map[string]any{"type": "string"}, "mermaid": map[string]any{"type": "string"},
			"title": map[string]any{"type": "string"},
		})
	case SchemaDiagramCollection:
		return object(map[string]any{
			"diagrams": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})
	case SchemaPlan:
		return object(map[string]any{
			"plan_id": map[string]any{"type": "string"}, "goal": map[string]any{"type": "string"},
			"status": map[string]any{"type": "string"}, "tasks": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"progress": map[string]any{"type": "object"},
		})
	case SchemaPlanTask:
		return object(map[string]any{
			"plan_id": map[string]any{"type": "string"}, "task_id": map[string]any{"type": "string"},
			"status": map[string]any{"type": "string"}, "task": map[string]any{"type": "object"},
			"evidence": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})
	case SchemaDelivery:
		return object(map[string]any{
			"plan_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"},
			"summary": map[string]any{"type": "string"}, "checks": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})
	default:
		return object(map[string]any{"values": stringArray})
	}
}

var outputSchemaCache struct {
	sync.Once
	value json.RawMessage
}

func OutputSchema() json.RawMessage {
	outputSchemaCache.Do(func() {
		outputSchemaCache.value = buildOutputSchema()
	})
	return append(json.RawMessage(nil), outputSchemaCache.value...)
}

func buildOutputSchema() json.RawMessage {
	resultTypes := []string{
		"text", "markdown", "search_result", "code_change", "table", "file_tree", "log", "error",
		"diagram", "diagram_collection", "plan", "plan_task", "delivery",
	}
	typeValues := make([]any, 0, len(resultTypes))
	schemaValues := make([]any, 0, len(resultTypes))
	definitions := map[string]any{}
	dataRefs := make([]any, 0, len(resultTypes))
	for _, resultType := range resultTypes {
		typeValues = append(typeValues, resultType)
		schemaName := schemaForType(resultType)
		schemaValues = append(schemaValues, schemaName)
		definitionName := strings.TrimPrefix(schemaName, "mcpx.")
		definitions[definitionName] = resultDataSchema(schemaName)
		dataRefs = append(dataRefs, map[string]any{"$ref": "#/$defs/" + definitionName})
	}
	schema := map[string]any{
		"$id": "mcpx.envelope.v1", "$defs": definitions, "type": "object", "required": []string{"mcpx"},
		"properties": map[string]any{
			"mcpx": map[string]any{
				"type": "object", "required": []string{"version", "trace", "result"},
				"properties": map[string]any{
					"version": map[string]any{"const": Version},
					"trace": map[string]any{
						"type": "object", "required": []string{"trace_id", "span_id", "source", "tool", "started_at_ms", "received_at_ms", "completed_at_ms", "network_latency_ms", "duration"},
					},
					"result": map[string]any{
						"type": "object", "required": []string{"type", "schema", "data"},
						"properties": map[string]any{
							"type": map[string]any{"type": "string", "enum": typeValues}, "schema": map[string]any{"type": "string", "enum": schemaValues},
							"summary": map[string]any{"type": "string"}, "data": map[string]any{"oneOf": dataRefs},
							"presentation": map[string]any{
								"type": "object", "required": []string{"default", "available", "fallback"},
								"properties": map[string]any{
									"default": map[string]any{"type": "string"}, "available": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
									"fallback": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "options": map[string]any{"type": "object"},
								},
							},
							"hints":   map[string]any{"type": "object", "properties": map[string]any{"preferred_behavior": map[string]any{"type": "string", "enum": []string{"show_directly", "summarize", "ask_confirm", "continue"}}}},
							"actions": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"id", "type", "label", "confirm", "arguments"}}},
						},
					},
				},
			},
		},
	}
	encoded, _ := json.Marshal(schema)
	return encoded
}
