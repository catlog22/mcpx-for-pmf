package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

func (r *Runtime) toolArtifactRegister(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}
	path, _ := envReq.Payload["path"].(string)
	if security.MatchFile(r.effectiveConfig(remote.WorkspacePath).Security.Files, path) != security.Allow {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "denied", "artifact path denied by file policy")
	}
	name, _ := envReq.Payload["name"].(string)
	kind, _ := envReq.Payload["kind"].(string)
	mimeType, _ := envReq.Payload["mime_type"].(string)
	if kind != "" && !validArtifactKind(kind) {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "invalid_kind", "unsupported artifact kind")
	}
	registered, err := r.artifacts.Register(ctx, remote.ID, principal.ID, remote.WorkspacePath, path, name, kind, mimeType)
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "artifact_register_error", err.Error())
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{RemoteSessionID: remote.ID, Type: "artifact.registered", OperationID: registered.ID, Summary: registered.Name, ResourceURI: registered.ResourceURI})
	result, err := r.remoteResult(envReq, remote.ID, remote.WorkspaceName, registered)
	if err == nil {
		link := mcp.NewResourceLink(registered.ResourceURI, registered.Name, "Registered MCPX development artifact", registered.MIMEType)
		link.Size = &registered.Size
		result.Content = append(result.Content, link)
		result.StructuredContent = registered
	}
	return result, err
}

func (r *Runtime) toolArtifactList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	kind, _ := envReq.Payload["kind"].(string)
	items, err := r.artifacts.List(ctx, remote.ID, kind, intPayload(envReq.Payload, "limit"))
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "artifact_list_error", err.Error())
	}
	return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, map[string]any{"artifacts": items})
}

func (r *Runtime) toolArtifactRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	artifactID, _ := envReq.Payload["artifact_id"].(string)
	read, err := r.artifacts.Read(ctx, remote.ID, artifactID, remote.WorkspacePath, int64(intPayload(envReq.Payload, "offset")), intPayload(envReq.Payload, "limit"))
	if err != nil {
		return r.terminalError(envReq, remote.ID, remote.WorkspaceName, "artifact_read_error", err.Error())
	}
	data := map[string]any{
		"artifact": read.Artifact, "offset": read.Offset, "next_offset": read.Next,
		"eof": read.EOF, "encoding": read.Encoding, "data": read.Data,
	}
	if !read.EOF {
		data["next_action"] = nextAction("artifact_manage", map[string]any{
			"action": "read", "remote_session_id": remote.ID, "artifact_id": artifactID, "offset": read.Next, "limit": intPayload(envReq.Payload, "limit"),
		})
	}
	return compactToolResult(data, fmt.Sprintf("Read artifact %s at byte offset %d.", artifactID, read.Offset)), nil
}

func (r *Runtime) resourceArtifact(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	remoteSessionID, artifactID, err := parseArtifactURI(req.Params.URI)
	if err != nil {
		return nil, err
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}
	remote, err := r.remote.Get(ctx, principal, remoteSessionID)
	if err != nil {
		return nil, err
	}
	registered, content, err := r.artifacts.ReadAll(ctx, remote.ID, artifactID, remote.WorkspacePath, 8<<20)
	if err != nil {
		return nil, err
	}
	if utf8.Valid(content) && artifactTextMIME(registered.MIMEType) {
		return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: registered.MIMEType, Text: string(content)}}, nil
	}
	return []mcp.ResourceContents{mcp.BlobResourceContents{URI: req.Params.URI, MIMEType: registered.MIMEType, Blob: base64.StdEncoding.EncodeToString(content)}}, nil
}

func parseArtifactURI(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "mcpx" || parsed.Host != "remote-sessions" {
		return "", "", fmt.Errorf("invalid artifact resource URI")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "artifacts" || parts[2] == "" {
		return "", "", fmt.Errorf("invalid artifact resource URI")
	}
	return parts[0], parts[2], nil
}

func validArtifactKind(value string) bool {
	switch value {
	case "test_report", "coverage", "build", "screenshot", "log", "other":
		return true
	default:
		return false
	}
}

func artifactTextMIME(value string) bool {
	return strings.HasPrefix(value, "text/") || strings.Contains(value, "json") || strings.Contains(value, "xml") || strings.Contains(value, "yaml")
}
