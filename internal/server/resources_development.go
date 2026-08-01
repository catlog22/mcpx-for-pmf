package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

func (r *Runtime) resourceChangesetDiff(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	remoteSessionID, changesetID, err := parseDevelopmentResourceURI(req.Params.URI, "changesets", "diff")
	if err != nil {
		return nil, err
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}
	if _, err := r.remote.Get(ctx, principal, remoteSessionID); err != nil {
		return nil, err
	}
	changeset, err := r.changesets.Get(ctx, changesetID)
	if err != nil || changeset.RemoteSessionID != remoteSessionID {
		return nil, fmt.Errorf("changeset not found")
	}
	if len(changeset.UnifiedDiff) > 8<<20 {
		return nil, fmt.Errorf("changeset diff exceeds resource limit")
	}
	return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: "text/x-diff", Text: changeset.UnifiedDiff}}, nil
}

func (r *Runtime) resourceTaskLogs(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	remoteSessionID, taskID, err := parseDevelopmentResourceURI(req.Params.URI, "tasks", "logs")
	if err != nil {
		return nil, err
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}
	if _, err := r.remote.Get(ctx, principal, remoteSessionID); err != nil {
		return nil, err
	}
	task, err := r.tasks.Get(remoteSessionID, taskID)
	if err != nil {
		return nil, err
	}
	logs, err := task.ReadAllLogs(8 << 20)
	if err != nil {
		return nil, err
	}
	return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: "text/plain", Text: string(logs)}}, nil
}

func parseDevelopmentResourceURI(value, collection, suffix string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "mcpx" || parsed.Host != "remote-sessions" {
		return "", "", fmt.Errorf("invalid development resource URI")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] != collection || parts[2] == "" || parts[3] != suffix {
		return "", "", fmt.Errorf("invalid development resource URI")
	}
	return parts[0], parts[2], nil
}
