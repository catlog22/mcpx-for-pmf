package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mcpx/internal/terminal"
)

// Execute runs skill entry via Runtime-controlled terminal bridge (not raw shell from skill code path).
func Execute(ctx context.Context, sk Skill, workDir string, arguments any) (map[string]any, error) {
	argsJSON, _ := json.Marshal(arguments)
	entry := sk.Manifest.Entry
	// Documentation / Agent skills: return markdown body, do not exec.
	if sk.Manifest.Runtime == "markdown" || sk.Manifest.Format == "skill_md" {
		path := filepath.Join(sk.Dir, entry)
		if entry == "" {
			path = filepath.Join(sk.Dir, "SKILL.md")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			// try skill.md
			body, err = os.ReadFile(filepath.Join(sk.Dir, "skill.md"))
		}
		if err != nil {
			return nil, fmt.Errorf("read skill doc: %w", err)
		}
		return map[string]any{
			"name":              sk.Manifest.Name,
			"runtime":           "markdown",
			"description":       sk.Manifest.Description,
			"content":           string(body),
			"path":              path,
			"working_directory": workDir,
		}, nil
	}
	var cmd string
	switch sk.Manifest.Runtime {
	case "node", "js", "javascript":
		cmd = fmt.Sprintf("cd %q && node %q", sk.Dir, entry)
	case "python", "python3", "":
		cmd = fmt.Sprintf("cd %q && python3 %q", sk.Dir, entry)
	default:
		return nil, fmt.Errorf("unsupported runtime %q", sk.Manifest.Runtime)
	}
	// pass args via env
	res, err := terminal.Exec(ctx, terminal.ExecOptions{
		WorkDir:  workDir,
		Command:  cmd,
		Timeout:  120 * time.Second,
		ExtraEnv: []string{"MCPX_SKILL_ARGS=" + string(argsJSON)},
	})
	out := map[string]any{
		"name":              sk.Manifest.Name,
		"command":           cmd,
		"working_directory": workDir,
		"exit_code":         res.ExitCode,
		"stdout":            res.Stdout,
		"stderr":            res.Stderr,
		"duration_ms":       res.DurationMs,
	}
	if err != nil && res.ExitCode == -1 {
		return out, err
	}
	return out, nil
}
