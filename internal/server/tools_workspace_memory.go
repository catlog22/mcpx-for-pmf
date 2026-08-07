package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/observation"
)

func (r *Runtime) toolWorkspaceMemory(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	keyword, _ := envReq.Payload["keyword"].(string)
	id, _ := envReq.Payload["id"].(string)
	date, _ := envReq.Payload["time"].(string)
	latest, err := memoryLatest(envReq.Payload["latest"])
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "bad_request", err.Error())
	}
	page, err := r.observation.store.QueryMemory(ctx, observation.MemoryQuery{
		Workspace: session.WorkspaceName,
		Keyword:   keyword,
		ID:        id,
		Time:      date,
		Latest:    latest,
	})
	if err != nil {
		code := "memory_query_error"
		if errors.Is(err, observation.ErrInvalidMemoryQuery) {
			code = "bad_request"
		}
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, code, err.Error())
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, page)
}

func memoryLatest(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	var number int64
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		number = typed
	case float64:
		if typed != float64(int64(typed)) {
			return 0, fmt.Errorf("latest must be an integer")
		}
		number = int64(typed)
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("latest must be an integer")
		}
		number = parsed
	default:
		return 0, fmt.Errorf("latest must be an integer")
	}
	if number > int64(^uint(0)>>1) || number < -int64(^uint(0)>>1)-1 {
		return 0, fmt.Errorf("latest is out of range")
	}
	return int(number), nil
}
