package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/changeset"
	"mcpx/internal/envelope"
	"mcpx/internal/logging"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
	"mcpx/internal/terminal"
)

const observationSummaryMaxBytes = 8 << 10
const observationWriteTimeout = 2 * time.Second

// observationBridge is the single write boundary for the workspace observer.
// Store.Append always happens before Broker.Publish so a live observer can
// recover every event from SQLite after a disconnect or buffer overflow.
type observationBridge struct {
	store   *observation.Store
	broker  *observation.Broker
	resolve func(context.Context, envelope.Request) (string, string)
}

func (b *observationBridge) Record(ctx context.Context, event observation.Event) error {
	if b == nil || b.store == nil || b.broker == nil {
		return nil
	}
	event.Workspace = strings.TrimSpace(event.Workspace)
	event.Intent = observation.SanitizeIntent(event.Intent)
	event.Summary, _ = observation.SanitizeText(event.Summary, observationSummaryMaxBytes)
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
	writeCtx, cancel := context.WithTimeout(observationContext(ctx), observationWriteTimeout)
	defer cancel()
	persisted, err := b.store.Append(writeCtx, event)
	if err != nil {
		logging.With("component", "workspace_observer").Error("persist event failed",
			"workspace", event.Workspace, "type", event.Type, "err", err)
		return err
	}
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

func (b *observationBridge) RecordToolStarted(ctx context.Context, name string, req envelope.Request, args map[string]any) error {
	workspace, remoteID := b.target(ctx, req)
	input, truncated := observation.NormalizeToolInput(args, observation.MaxEventBytes)
	return b.Record(ctx, observation.Event{
		Workspace:       workspace,
		RemoteSessionID: remoteID,
		RequestID:       req.RequestID,
		Tool:            name,
		Type:            observation.TypeToolStarted,
		Intent:          req.Intent,
		Input:           input,
		Truncated:       truncated,
	})
}

func (b *observationBridge) RecordToolCompleted(ctx context.Context, name string, req envelope.Request, result *mcp.CallToolResult, callErr error, timing interactionTiming) error {
	workspace, remoteID := b.target(ctx, req)
	resultJSON, truncated := observation.NormalizeToolOutput(result, observation.MaxEventBytes)
	var resultValue any
	if err := json.Unmarshal(resultJSON, &resultValue); err != nil {
		resultValue = map[string]any{"available": false}
	}
	status := "ok"
	if callErr != nil || result == nil || result.IsError {
		status = "error"
	}
	outputValue := map[string]any{
		"status": status,
		"timing": map[string]any{
			"started_at_ms":     timing.StartedAtMs,
			"received_at_ms":    timing.ReceivedAtMs,
			"completed_at_ms":   timing.CompletedAtMs,
			"processing_ms":     timing.ProcessingMs,
			"server_elapsed_ms": timing.ServerElapsedMs,
		},
		"result": resultValue,
	}
	if callErr != nil {
		outputValue["error"] = observation.RedactText(callErr.Error())
	}
	encoded, encodeErr := json.Marshal(outputValue)
	if encodeErr != nil {
		encoded = []byte(`{"status":"error","result":{"available":false}}`)
		truncated = true
	}
	return b.Record(ctx, observation.Event{
		Workspace:       workspace,
		RemoteSessionID: remoteID,
		RequestID:       req.RequestID,
		Tool:            name,
		Type:            observation.TypeToolCompleted,
		Intent:          req.Intent,
		Output:          encoded,
		Summary:         fmt.Sprintf("%s %s", name, status),
		Truncated:       truncated,
	})
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
		Output:          encoded,
		Summary:         summary,
		ResourceURI:     event.ResourceURI,
		Truncated:       truncated,
		CreatedAt:       event.CreatedAt,
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
		Intent:          req.Intent,
		Output:          bounded,
		Summary:         item.Summary,
		ResourceURI:     fmt.Sprintf("mcpx://remote-sessions/%s/changesets/%s/diff", session.ID, item.ID),
		Truncated:       truncated,
	})
}

func (r *Runtime) observeTaskOutput(chunk terminal.OutputChunk) {
	if r == nil || r.observation == nil || len(chunk.Data) == 0 {
		return
	}
	text, truncated := observation.SanitizeText(string(chunk.Data), observation.MaxEventBytes)
	encoded, err := json.Marshal(map[string]any{
		"text":  text,
		"bytes": len(chunk.Data),
	})
	if err != nil {
		encoded = []byte(`{"text":"[UNAVAILABLE]","bytes":0}`)
		truncated = true
	}
	_ = r.observation.Record(context.Background(), observation.Event{
		Workspace:       chunk.WorkspaceName,
		RemoteSessionID: chunk.RemoteSessionID,
		OperationID:     chunk.TaskID,
		Type:            observation.TypeCommandOutput,
		Output:          encoded,
		Summary:         fmt.Sprintf("task %s %s output", chunk.TaskID, chunk.Stream),
		ResourceURI:     fmt.Sprintf("mcpx://remote-sessions/%s/tasks/%s/logs", chunk.RemoteSessionID, chunk.TaskID),
		Stream:          chunk.Stream,
		Offset:          chunk.Offset,
		Truncated:       truncated,
	})
}

func marshalObservationValue(value any) ([]byte, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"value":"[UNAVAILABLE]"}`), true
	}
	return observation.SanitizeJSON(encoded, observation.MaxEventBytes)
}
