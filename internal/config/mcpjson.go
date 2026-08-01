package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// LoadMCPFile reads one .mcp.json; missing => empty servers.
func LoadMCPFile(path string) (MCPFile, error) {
	out := MCPFile{MCPServers: map[string]MCPServer{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("parse %s: %w", path, err)
	}
	if out.MCPServers == nil {
		out.MCPServers = map[string]MCPServer{}
	}
	return out, nil
}

// MergeMCP merges global then project by server name (project wins).
func MergeMCP(global, project MCPFile) MCPFile {
	out := MCPFile{MCPServers: map[string]MCPServer{}}
	for k, v := range global.MCPServers {
		out.MCPServers[k] = v
	}
	for k, v := range project.MCPServers {
		out.MCPServers[k] = v
	}
	return out
}

// LoadMergedMCP loads global + project MCP JSON for a workspace.
func LoadMergedMCP(workspacePath string) (MCPFile, error) {
	gPath, err := GlobalMCPPath()
	if err != nil {
		return MCPFile{}, err
	}
	g, err := LoadMCPFile(gPath)
	if err != nil {
		return MCPFile{}, err
	}
	p, err := LoadMCPFile(ProjectMCPPath(workspacePath))
	if err != nil {
		return MCPFile{}, err
	}
	return MergeMCP(g, p), nil
}
