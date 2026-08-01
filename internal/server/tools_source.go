package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/security"
)

func (r *Runtime) toolProjectInspect(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, session, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	data := inspectProject(ctx, session.WorkspacePath)
	data["agent_instructions"] = r.agentInstructions(session.WorkspacePath)
	result, err := r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
	if err == nil {
		result.StructuredContent = data
	}
	return result, err
}

func (r *Runtime) sourcePathAllowed(workspacePath string) func(string) bool {
	rules := r.effectiveConfig(workspacePath).Security.Files
	return func(path string) bool { return security.MatchFile(rules, path) == security.Allow }
}

func (r *Runtime) sourceError(envReq envelope.Request, remoteSessionID, workspace string, err error) (*mcp.CallToolResult, error) {
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, workspace, nil, "source_error", err.Error())
	response.RemoteSessionID = remoteSessionID
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "no such file") || strings.Contains(message, "not found") || strings.Contains(message, "does not exist") {
		addRecoveryAction(&response, "context_query", "locate the file or directory before retrying the read", map[string]any{
			"remote_session_id": remoteSessionID,
			"action":            "list",
		})
	}
	return r.resultJSON(response)
}

func inspectProject(ctx context.Context, root string) map[string]any {
	manifestStacks := map[string]string{
		"go.mod": "go", "package.json": "node", "Cargo.toml": "rust", "pyproject.toml": "python",
		"requirements.txt": "python", "pom.xml": "java-maven", "build.gradle": "java-gradle",
		"composer.json": "php", "Gemfile": "ruby", "Makefile": "make",
	}
	var manifests, stacks []string
	for manifest, stack := range manifestStacks {
		if info, err := os.Stat(filepath.Join(root, manifest)); err == nil && info.Mode().IsRegular() {
			manifests = append(manifests, manifest)
			stacks = append(stacks, stack)
		}
	}
	sort.Strings(manifests)
	sort.Strings(stacks)
	var instructions []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "CODEX.md", "CONTRIBUTING.md", "README.md"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
			instructions = append(instructions, name)
		}
	}
	entryCandidates := []string{"main.go", "cmd", "src/main.go", "src/index.ts", "src/index.js", "app", "pages", "manage.py"}
	var entrypoints []string
	for _, name := range entryCandidates {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err == nil {
			entrypoints = append(entrypoints, name)
		}
	}
	tasks := map[string]string{}
	if content, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var packageFile struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(content, &packageFile) == nil {
			for name := range packageFile.Scripts {
				tasks[name] = "npm run " + name
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		tasks["test"] = "go test ./..."
		tasks["build"] = "go build ./..."
	}
	gitStatus := boundedCommand(ctx, root, "git", "status", "--short", "--branch")
	return map[string]any{
		"stacks": stacks, "manifests": manifests, "entrypoints": entrypoints,
		"instructions": instructions, "tasks": tasks, "git_status": gitStatus,
	}
}

func boundedCommand(parent context.Context, workDir, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = workDir
	output, err := command.CombinedOutput()
	if err != nil && len(output) == 0 {
		return ""
	}
	value := strings.TrimSpace(string(output))
	if len(value) > 20_000 {
		value = value[:20_000]
	}
	return value
}
