package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/operation"
)

func executionMode(req mcp.CallToolRequest) string {
	mode, _ := req.GetArguments()["execution_mode"].(string)
	return strings.ToLower(strings.TrimSpace(mode))
}

func asyncEligibleTool(name string) bool {
	switch name {
	case "workspace_list", "session_open", "operation_batch", "operation_manage":
		return false
	default:
		return true
	}
}

func (r *Runtime) submitAsyncTool(ctx context.Context, name string, req mcp.CallToolRequest, envReq envelope.Request) (*mcp.CallToolResult, error) {
	if r.operations == nil {
		return r.terminalError(envReq, envReq.RemoteSessionID, envReq.Workspace, "operation_unavailable", "asynchronous operations are unavailable")
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return r.terminalError(envReq, "", envReq.Workspace, "unauthorized", "invalid or missing token")
	}
	remoteID := strings.TrimSpace(envReq.RemoteSessionID)
	if remoteID == "" {
		return r.terminalError(envReq, "", envReq.Workspace, "remote_session_required", "remote session id required for asynchronous execution")
	}
	session, err := r.remote.Get(ctx, principal, remoteID)
	if err != nil {
		return r.remoteError(envReq, remoteID, envReq.Workspace, err)
	}
	arguments := cloneArguments(req.GetArguments())
	delete(arguments, "execution_mode")
	record, err := r.operations.Submit(ctx, operation.SubmitSpec{
		RemoteSessionID: session.ID,
		WorkspaceName:   session.WorkspaceName,
		RequestID:       envReq.RequestID,
		Purpose:         envReq.Intent,
		Steps:           []operation.StepSpec{{ID: "main", Tool: name, Arguments: arguments, Exclusive: r.toolMeta[name].OpenWorld || !r.toolMeta[name].ReadOnly}},
	}, r.executeOperationStep)
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "operation_submit_error", err.Error())
	}
	response := envelope.Accepted(envReq.RequestID, session.WorkspaceName, map[string]any{
		"operation_id": record.ID,
		"state":        record.State,
		"tool":         name,
	})
	response.RemoteSessionID = session.ID
	return r.resultJSON(response)
}

func (r *Runtime) executeOperationStep(ctx context.Context, input operation.ExecuteInput) operation.ExecuteResult {
	r.toolIndexMu.RLock()
	handler := r.toolHandlers[input.Tool]
	r.toolIndexMu.RUnlock()
	if handler == nil {
		return operation.ExecuteResult{Err: fmt.Errorf("tool %q is not registered", input.Tool)}
	}
	arguments := cloneArguments(input.Arguments)
	arguments["session_id"] = input.RemoteSessionID
	arguments["purpose"] = input.Purpose
	arguments["execution_mode"] = "sync"
	request := mcp.CallToolRequest{}
	request.Params.Arguments = arguments
	childCtx := r.operationChildContext(ctx, input)
	result, callErr := handler(childCtx, request)
	return operationResult(result, callErr)
}

func (r *Runtime) operationChildContext(ctx context.Context, input operation.ExecuteInput) context.Context {
	runtime, _ := runtimeContextFrom(ctx)
	runtime.RequestID = input.RequestID
	runtime.OperationID = input.OperationID
	runtime.ParentOperationID = input.OperationID
	runtime.StepID = input.StepID
	runtime.StartedAtMs = time.Now().UnixMilli()
	runtime.ReceivedAtMs = runtime.StartedAtMs
	return withOperationChild(withRuntimeContext(ctx, runtime))
}

func operationResult(result *mcp.CallToolResult, callErr error) operation.ExecuteResult {
	encoded, err := json.Marshal(result)
	if err != nil {
		encoded = []byte(`{"available":false}`)
		callErr = errors.Join(callErr, err)
	}
	output := operation.ExecuteResult{Result: encoded, Err: callErr}
	if result == nil {
		if output.Err == nil {
			output.Err = errors.New("tool returned no result")
		}
		return output
	}
	status := publicResultStatus(result)
	if status == "" {
		status = operationResultStatus(result)
	}
	if token := resultConfirmationToken(result); token != "" {
		output.WaitingConfirmation = status == string(envelope.StatusNeedConfirmation)
		output.ConfirmationToken = token
	}
	if output.Err == nil && status == "failed" {
		output.Err = errors.New("public tool execution failed")
	}
	return output
}

func operationResultStatus(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		textContent, ok := content.(mcp.TextContent)
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(textContent.Text), &value) == nil {
			if status := findStatusValue(value); status != "" {
				return status
			}
		}
	}
	return ""
}

func findStatusValue(value any) string {
	mapValue, ok := value.(map[string]any)
	if !ok {
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if status := findStatusValue(item); status != "" {
					return status
				}
			}
		}
		return ""
	}
	if status, ok := mapValue["status"].(string); ok {
		switch status {
		case "succeeded", "accepted", "waiting_confirmation", "interrupted", "failed", "ok":
			return status
		}
	}
	for _, key := range []string{"mcpx", "result", "data", "error"} {
		if nested, exists := mapValue[key]; exists {
			if status := findStatusValue(nested); status != "" {
				return status
			}
		}
	}
	return ""
}

func resultConfirmationToken(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		textContent, ok := content.(mcp.TextContent)
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(textContent.Text), &value) == nil {
			if token := findStringValue(value, "confirmation_token"); token != "" {
				return token
			}
		}
	}
	return ""
}

func findStringValue(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if text, ok := typed[key].(string); ok {
			return strings.TrimSpace(text)
		}
		for _, nested := range typed {
			if result := findStringValue(nested, key); result != "" {
				return result
			}
		}
	case []any:
		for _, nested := range typed {
			if result := findStringValue(nested, key); result != "" {
				return result
			}
		}
	}
	return ""
}

func cloneArguments(input map[string]any) map[string]any {
	output := make(map[string]any, len(input)+3)
	for key, value := range input {
		output[key] = value
	}
	return output
}
