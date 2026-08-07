package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/changeset"
	"mcpx/internal/envelope"
	"mcpx/internal/logging"
	"mcpx/internal/observation"
	"mcpx/internal/operation"
	"mcpx/internal/remotesession"
	"mcpx/internal/terminal"
)

const observationSummaryMaxBytes = 8 << 10

// observationWriteTimeout bounds SQLite append only. Must not inherit the
// tools/call request deadline (WithoutCancel still preserves deadlines), or
// busy DB writes surface as "context deadline exceeded" after OAuth/session.
const observationWriteTimeout = 10 * time.Second

type observationTaskStreamKey struct {
	taskID          string
	remoteSessionID string
	stream          string
}

// observationBridge is the single write boundary for the workspace observer.
// Store.Append always happens before Broker.Publish so a live observer can
// recover every event from SQLite after a disconnect or buffer overflow.
// Tool start/complete events are enqueued on async so tools/call is not blocked.
type observationBridge struct {
	store           *observation.Store
	broker          *observation.Broker
	async           *observation.AsyncRecorder
	resolve         func(context.Context, envelope.Request) (string, string)
	outputStateMu   sync.Mutex
	outputSanitizer map[observationTaskStreamKey]*observation.TextStreamSanitizer
}

func (b *observationBridge) Record(ctx context.Context, event observation.Event) error {
	if b == nil || b.store == nil || b.broker == nil {
		return nil
	}
	event.Workspace = strings.TrimSpace(event.Workspace)
	event.Intent = observation.SanitizeIntent(event.Intent)
	event.Purpose = observation.SanitizeIntent(event.Purpose)
	event.Summary, _ = observation.SanitizeText(event.Summary, observationSummaryMaxBytes)
	event.ProgressSummary, _ = observation.SanitizeText(event.ProgressSummary, observationSummaryMaxBytes)
	if len(event.Input) > 0 {
		var truncated bool
		event.Input, truncated = observation.SanitizeJSON(event.Input, observation.MaxEventBytes)
		event.Truncated = event.Truncated || truncated
	}
	if len(event.Output) > 0 {
		var truncated bool
		event.Output, truncated = observation.SanitizeJSON(event.Output, observation.MaxEventBytes)
		event.Truncated = event.Truncated || truncated
	}
	// Always use an independent background budget so async/sync observation
	// never fails because the HTTP request context already expired.
	writeCtx, cancel := context.WithTimeout(context.Background(), observationWriteTimeout)
	defer cancel()
	persisted, err := b.store.Append(writeCtx, event)
	if err != nil {
		logging.With("component", "workspace_observer").Error("persist event failed",
			"workspace", event.Workspace, "type", event.Type, "err", err)
		return err
	}
	_ = ctx // retained for call-site symmetry; resolution uses observationContext elsewhere
	b.broker.Publish(persisted)
	return nil
}

func (b *observationBridge) target(ctx context.Context, request envelope.Request) (string, string) {
	workspace := strings.TrimSpace(request.Workspace)
	remoteSessionID := remoteSessionID(request)
	if workspace == "" && b != nil && b.resolve != nil {
		workspace, remoteSessionID = b.resolve(observationContext(ctx), request)
	}
	return strings.TrimSpace(workspace), strings.TrimSpace(remoteSessionID)
}

func observationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func (b *observationBridge) enqueueOrRecord(ctx context.Context, event observation.Event) error {
	if b != nil && b.async != nil {
		b.async.Enqueue(event)
		return nil
	}
	return b.Record(ctx, event)
}

func (b *observationBridge) RecordToolStarted(ctx context.Context, name string, req envelope.Request, args map[string]any) error {
	workspace, remoteID := b.target(ctx, req)
	input, truncated := observation.NormalizeToolInput(args, observation.MaxEventBytes)
	return b.enqueueOrRecord(ctx, observation.Event{
		Workspace:         workspace,
		RemoteSessionID:   remoteID,
		RequestID:         req.RequestID,
		OperationID:       req.OperationID,
		ParentOperationID: req.ParentOperationID,
		StepID:            req.StepID,
		Tool:              name,
		Type:              observation.TypeToolStarted,
		Status:            "started",
		Purpose:           req.Intent,
		Intent:            req.Intent,
		ProgressSummary:   req.ProgressSummary,
		Input:             input,
		Truncated:         truncated,
	})
}

func (b *observationBridge) RecordToolCompleted(ctx context.Context, name string, req envelope.Request, args map[string]any, result *mcp.CallToolResult, callErr error, timing interactionTiming) error {
	workspace, remoteID := b.target(ctx, req)
	input, inputTruncated := observation.NormalizeToolInput(args, observation.MaxEventBytes)
	status := "succeeded"
	if callErr != nil || result == nil || result.IsError {
		status = "failed"
	} else if responseStatus := publicResultStatus(result); responseStatus != "" {
		status = responseStatus
	}
	facts := toolObservationFacts(name, args, result, timing)
	summary := firstToolText(result)
	if summary == "" {
		summary = fmt.Sprintf("%s %s", name, status)
	}
	if callErr != nil {
		summary = observation.RedactText(callErr.Error())
	}
	snap := observation.HumanObsSnapshot{
		Tool:             name,
		Status:           status,
		Purpose:          req.Intent,
		Intent:           req.Intent,
		ProgressSummary:  req.ProgressSummary,
		Summary:          summary,
		Command:          facts.Command,
		WorkingDirectory: facts.WorkingDirectory,
		ExitCode:         facts.ExitCode,
		DurationMs:       facts.DurationMs,
		Path:             facts.Path,
		ResourceURI:      facts.ResourceURI,
		InputRedacted:    input,
		Truncated:        inputTruncated,
	}
	output, outTruncated := observation.NormalizeHumanToolOutput(snap, observation.MaxEventBytes)
	return b.enqueueOrRecord(ctx, observation.Event{
		Workspace:         workspace,
		RemoteSessionID:   remoteID,
		RequestID:         req.RequestID,
		OperationID:       req.OperationID,
		ParentOperationID: req.ParentOperationID,
		StepID:            req.StepID,
		Tool:              name,
		Type:              observation.TypeToolCompleted,
		Status:            status,
		Purpose:           req.Intent,
		Intent:            req.Intent,
		ProgressSummary:   req.ProgressSummary,
		Input:             input,
		Output:            output,
		Summary:           fmt.Sprintf("%s %s", name, status),
		Command:           facts.Command,
		WorkingDirectory:  facts.WorkingDirectory,
		ExitCode:          facts.ExitCode,
		DurationMs:        facts.DurationMs,
		SkillName:         facts.SkillName,
		MCPServer:         facts.MCPServer,
		MCPTool:           facts.MCPTool,
		Path:              facts.Path,
		ResourceURI:       facts.ResourceURI,
		Truncated:         inputTruncated || outTruncated,
	})
}

func publicResultStatus(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	if sc, ok := result.StructuredContent.(map[string]any); ok {
		if status, _ := sc["status"].(string); status != "" {
			switch status {
			case string(envelope.StatusOK), string(envelope.StatusAccepted), string(envelope.StatusNeedConfirmation), string(envelope.StatusInterrupted):
				return status
			case string(envelope.StatusError): // wire value "failed"
				return "failed"
			}
		}
	}
	return operationResultStatus(result)
}

func firstToolText(result *mcp.CallToolResult) string {
	return mcpresult.FirstText(result)
}

func toolObservationFacts(name string, args map[string]any, result *mcp.CallToolResult, timing interactionTiming) observation.Event {
	facts := observation.Event{DurationMs: timing.ServerElapsedMs}
	if command, ok := args["command"].(string); ok {
		facts.Command = strings.TrimSpace(command)
	}
	if task, ok := args["task"].(string); ok && facts.Command == "" {
		facts.Command = strings.TrimSpace(task)
	}
	if path, ok := args["path"].(string); ok {
		facts.Path = strings.TrimSpace(path)
	}
	if name == "skill_call" {
		facts.SkillName, _ = args["name"].(string)
	}
	if name == "mcp_call" {
		facts.MCPServer, _ = args["server"].(string)
		facts.MCPTool, _ = args["tool"].(string)
	}
	// Pull a few human-useful fields from structured data only — never dump SC.
	data := businessDataFromResult(result)
	if workingDirectory, ok := data["working_directory"].(string); ok {
		facts.WorkingDirectory = workingDirectory
	}
	if command, ok := data["command"].(string); ok && facts.Command == "" {
		facts.Command = command
	}
	if duration, ok := numberValue(data["duration_ms"]); ok && duration >= 0 {
		facts.DurationMs = int64(duration)
	}
	if exitCode, ok := numberValue(data["exit_code"]); ok {
		code := int(exitCode)
		facts.ExitCode = &code
	}
	if path, ok := data["path"].(string); ok && facts.Path == "" {
		facts.Path = path
	}
	return facts
}

func businessDataFromResult(result *mcp.CallToolResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok || sc == nil {
		return map[string]any{}
	}
	if data, ok := sc["data"].(map[string]any); ok && data != nil {
		return data
	}
	return sc
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

func (r *Runtime) observationTarget(ctx context.Context, req envelope.Request) (string, string) {
	workspace := strings.TrimSpace(req.Workspace)
	remoteID := remoteSessionID(req)
	if workspace != "" || remoteID == "" || r == nil || r.remote == nil {
		return workspace, remoteID
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return workspace, remoteID
	}
	session, err := r.remote.Get(ctx, principal, remoteID)
	if err != nil {
		return workspace, remoteID
	}
	return session.WorkspaceName, remoteID
}

func (r *Runtime) observeRemoteEvent(session remotesession.Session, event remotesession.Event) {
	if r == nil || r.observation == nil {
		return
	}
	payload := map[string]any{
		"source_type":     event.Type,
		"source_sequence": event.Sequence,
		"metadata":        event.Metadata,
	}
	encoded, truncated := marshalObservationValue(payload)
	eventType := observation.TypeObserverNotice
	if strings.HasPrefix(event.Type, "remote_session.") {
		eventType = observation.TypeSessionLifecycle
	}
	summary := event.Summary
	if summary == "" {
		summary = event.Type
	} else {
		summary = event.Type + ": " + summary
	}
	_ = r.observation.Record(context.Background(), observation.Event{
		Workspace:       session.WorkspaceName,
		RemoteSessionID: session.ID,
		OperationID:     event.OperationID,
		Type:            eventType,
		Status:          "succeeded",
		Output:          encoded,
		Summary:         summary,
		ResourceURI:     event.ResourceURI,
		Truncated:       truncated,
		CreatedAt:       event.CreatedAt,
	})
}

func (r *Runtime) observeOperationEvent(event operation.Event) {
	if r == nil || r.observation == nil {
		return
	}
	typeName := event.Type
	parentOperationID := ""
	if event.StepID != "" {
		parentOperationID = event.OperationID
	}
	status := string(event.State)
	if event.State == operation.StateQueued || event.State == operation.StateRunning {
		status = "running"
	}
	_ = r.observation.Record(context.Background(), observation.Event{
		Workspace:         event.WorkspaceName,
		RemoteSessionID:   event.RemoteSessionID,
		RequestID:         event.RequestID,
		OperationID:       event.OperationID,
		ParentOperationID: parentOperationID,
		StepID:            event.StepID,
		Tool:              event.Tool,
		Type:              typeName,
		Status:            status,
		Summary:           event.Summary,
		CreatedAt:         event.CreatedAt,
	})
}

func (r *Runtime) observeAppliedChangeset(ctx context.Context, req envelope.Request, session remotesession.Session, item changeset.Changeset) {
	if r == nil || r.observation == nil {
		return
	}
	dto := changeSummaryDTO(item)
	encoded, err := json.Marshal(dto)
	if err != nil {
		encoded = []byte(`{"changeset_id":"","files":[],"truncated":true}`)
	}
	bounded, truncated := observation.SanitizeJSON(encoded, observation.MaxEventBytes)
	_ = r.observation.Record(ctx, observation.Event{
		Workspace:       session.WorkspaceName,
		RemoteSessionID: session.ID,
		RequestID:       req.RequestID,
		OperationID:     item.ID,
		Type:            observation.TypeFileChanged,
		Status:          "succeeded",
		Intent:          req.Intent,
		Output:          bounded,
		Summary:         item.Summary,
		ResourceURI:     fmt.Sprintf("mcpx://remote-sessions/%s/changesets/%s/diff", session.ID, item.ID),
		Truncated:       truncated,
	})
}

func (r *Runtime) observeTaskOutput(chunk terminal.OutputChunk) {
	if r == nil || r.observation == nil || (len(chunk.Data) == 0 && !chunk.Final) {
		return
	}
	text, truncated := r.observation.sanitizeTaskOutput(chunk)
	if text == "" && len(chunk.Data) == 0 {
		return
	}
	encoded, err := json.Marshal(map[string]any{
		"text":  text,
		"bytes": len(chunk.Data),
	})
	if err != nil {
		encoded = []byte(`{"text":"[UNAVAILABLE]","bytes":0}`)
		truncated = true
	}
	_ = r.observation.Record(context.Background(), observation.Event{
		Workspace:        chunk.WorkspaceName,
		RemoteSessionID:  chunk.RemoteSessionID,
		RequestID:        chunk.RequestID,
		OperationID:      chunk.TaskID,
		Tool:             chunk.Tool,
		Type:             observation.TypeCommandOutput,
		Status:           "running",
		Command:          chunk.Command,
		WorkingDirectory: chunk.WorkDir,
		Output:           encoded,
		Summary:          fmt.Sprintf("task %s %s output", chunk.TaskID, chunk.Stream),
		ResourceURI:      fmt.Sprintf("mcpx://remote-sessions/%s/tasks/%s/logs", chunk.RemoteSessionID, chunk.TaskID),
		Stream:           chunk.Stream,
		Offset:           chunk.Offset,
		Truncated:        truncated,
	})
}

func (b *observationBridge) sanitizeTaskOutput(chunk terminal.OutputChunk) (string, bool) {
	key := observationTaskStreamKey{
		taskID:          chunk.TaskID,
		remoteSessionID: chunk.RemoteSessionID,
		stream:          chunk.Stream,
	}
	b.outputStateMu.Lock()
	defer b.outputStateMu.Unlock()
	if b.outputSanitizer == nil {
		b.outputSanitizer = make(map[observationTaskStreamKey]*observation.TextStreamSanitizer)
	}
	sanitizer := b.outputSanitizer[key]
	if sanitizer == nil {
		sanitizer = &observation.TextStreamSanitizer{}
		b.outputSanitizer[key] = sanitizer
	}
	text, truncated := sanitizer.SanitizeChunk(string(chunk.Data), chunk.Final, observation.MaxEventBytes)
	if chunk.Final {
		delete(b.outputSanitizer, key)
	}
	return text, truncated
}

func marshalObservationValue(value any) ([]byte, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"value":"[UNAVAILABLE]"}`), true
	}
	return observation.SanitizeJSON(encoded, observation.MaxEventBytes)
}
