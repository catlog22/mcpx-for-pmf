package projecttask

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Task struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
}

type Diagnostic struct {
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func Discover(root string) []Task {
	byName := map[string]Task{}
	if content, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var manifest struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(content, &manifest) == nil {
			runner := nodeRunner(root)
			for name := range manifest.Scripts {
				byName[name] = Task{Name: name, Command: runner + " run " + name, Source: "package.json", Kind: taskKind(name)}
			}
		}
	}
	if regular(root, "go.mod") {
		addDefault(byName, Task{Name: "test", Command: "go test ./...", Source: "go.mod", Kind: "test"})
		addDefault(byName, Task{Name: "build", Command: "go build ./...", Source: "go.mod", Kind: "build"})
		addDefault(byName, Task{Name: "vet", Command: "go vet ./...", Source: "go.mod", Kind: "lint"})
	}
	if regular(root, "Cargo.toml") {
		addDefault(byName, Task{Name: "test", Command: "cargo test", Source: "Cargo.toml", Kind: "test"})
		addDefault(byName, Task{Name: "build", Command: "cargo build", Source: "Cargo.toml", Kind: "build"})
		addDefault(byName, Task{Name: "check", Command: "cargo check", Source: "Cargo.toml", Kind: "lint"})
	}
	if regular(root, "pyproject.toml") || regular(root, "pytest.ini") {
		addDefault(byName, Task{Name: "test", Command: "python -m pytest", Source: "pyproject.toml", Kind: "test"})
	}
	if content, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, ".") {
				continue
			}
			name, _, ok := strings.Cut(line, ":")
			name = strings.TrimSpace(name)
			if !ok || name == "" || strings.ContainsAny(name, " $%/") {
				continue
			}
			addDefault(byName, Task{Name: name, Command: "make " + name, Source: "Makefile", Kind: taskKind(name)})
		}
	}
	result := make([]Task, 0, len(byName))
	for _, task := range byName {
		result = append(result, task)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func Find(root, name string) (Task, bool) {
	for _, task := range Discover(root) {
		if task.Name == name {
			return task, true
		}
	}
	return Task{}, false
}

var diagnosticPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(.+?):(\d+):(\d+):\s*(?:(error|warning|info)\s*:\s*)?(.+)$`),
	regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s*(?:(error|warning|info)\s*)?(.+)$`),
	regexp.MustCompile(`^(.+?):(\d+):\s*(?:(error|warning|info)\s*:\s*)?(.+)$`),
}

func ParseDiagnostics(log string, limit int) []Diagnostic {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var diagnostics []Diagnostic
	seen := map[string]bool{}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		for _, pattern := range diagnosticPatterns {
			matches := pattern.FindStringSubmatch(line)
			if matches == nil {
				continue
			}
			diagnostic := diagnosticFromMatch(matches)
			key := diagnostic.Path + line
			if !seen[key] {
				seen[key] = true
				diagnostics = append(diagnostics, diagnostic)
			}
			break
		}
		if len(diagnostics) >= limit {
			break
		}
	}
	return diagnostics
}

func diagnosticFromMatch(matches []string) Diagnostic {
	diagnostic := Diagnostic{Path: filepath.ToSlash(matches[1]), Line: atoi(matches[2]), Severity: "error"}
	if len(matches) == 6 {
		diagnostic.Column = atoi(matches[3])
		if matches[4] != "" {
			diagnostic.Severity = matches[4]
		}
		diagnostic.Message = matches[5]
	} else {
		if matches[3] != "" {
			diagnostic.Severity = matches[3]
		}
		diagnostic.Message = matches[4]
	}
	return diagnostic
}

func addDefault(tasks map[string]Task, task Task) {
	if _, exists := tasks[task.Name]; !exists {
		tasks[task.Name] = task
	}
}

func nodeRunner(root string) string {
	switch {
	case regular(root, "pnpm-lock.yaml"):
		return "pnpm"
	case regular(root, "yarn.lock"):
		return "yarn"
	case regular(root, "bun.lock"), regular(root, "bun.lockb"):
		return "bun"
	default:
		return "npm"
	}
}

func regular(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && info.Mode().IsRegular()
}

func taskKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "test"):
		return "test"
	case strings.Contains(lower, "lint") || strings.Contains(lower, "check") || strings.Contains(lower, "vet"):
		return "lint"
	case strings.Contains(lower, "build") || strings.Contains(lower, "compile"):
		return "build"
	case strings.Contains(lower, "dev") || strings.Contains(lower, "serve") || strings.Contains(lower, "start"):
		return "serve"
	default:
		return "custom"
	}
}

func atoi(value string) int {
	result := 0
	for _, char := range value {
		result = result*10 + int(char-'0')
	}
	return result
}
