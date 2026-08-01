package server

import (
	"context"
	"encoding/base64"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/envelope"
	"mcpx/internal/screenshot"
)

type screenCapturer interface {
	Capture(context.Context, screenshot.Request) (screenshot.Result, error)
}

func (r *Runtime) toolScreenshotCapture(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	request := screenshot.Request{
		Mode: stringPayload(envReq.Payload, "mode"), Compression: stringPayload(envReq.Payload, "compression"),
		Format: stringPayload(envReq.Payload, "format"), Display: intPayload(envReq.Payload, "display"),
		X: intPayload(envReq.Payload, "x"), Y: intPayload(envReq.Payload, "y"),
		Width: intPayload(envReq.Payload, "width"), Height: intPayload(envReq.Payload, "height"),
		Quality: intPayload(envReq.Payload, "quality"), MaxWidth: intPayload(envReq.Payload, "max_width"),
		MaxHeight: intPayload(envReq.Payload, "max_height"),
	}
	captured, err := r.screenshot.Capture(ctx, request)
	if err != nil {
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, "screenshot_error", err.Error())
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	result, err := r.remoteResult(envReq, session.ID, session.WorkspaceName, captured.Metadata)
	if err != nil {
		return nil, err
	}
	result.Content = append(result.Content, mcp.ImageContent{
		Type: mcp.ContentTypeImage, Data: base64.StdEncoding.EncodeToString(captured.Data), MIMEType: captured.Metadata.MIMEType,
	})
	result.StructuredContent = captured.Metadata
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName, Tool: "screenshot_capture", Status: "ok", Detail: map[string]any{
		"mode": captured.Metadata.Mode, "display": captured.Metadata.Display,
		"width": captured.Metadata.OutputWidth, "height": captured.Metadata.OutputHeight,
		"format": captured.Metadata.Format, "bytes": captured.Metadata.Bytes, "sha256": captured.Metadata.SHA256,
	}})
	return result, nil
}

func stringPayload(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
