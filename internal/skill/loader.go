package skill

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mcpx/internal/config"
	"mcpx/internal/logging"
)

// Manifest describes a skill package (skill.yaml or SKILL.md frontmatter).
type Manifest struct {
	Name            string         `yaml:"name"`
	Description     string         `yaml:"description"`
	Runtime         string         `yaml:"runtime"`
	Entry           string         `yaml:"entry"`
	Permissions     []string       `yaml:"permissions"`
	ArgumentsSchema map[string]any `yaml:"arguments_schema"`
	// Format: yaml | skill_md
	Format string `yaml:"-"`
}

// Skill is a discovered skill package.
type Skill struct {
	Manifest Manifest
	Dir      string
	Source   string // scan root that contained it
}

// LoadAll scans dirs for skill packages.
// Recognizes:
//   - <name>/skill.yaml  (MCPX executable skill)
//   - <name>/SKILL.md or skill.md (Agent/Superpowers doc skill with YAML frontmatter)
//
// First occurrence of a name wins (callers should pass high-priority dirs first).
func LoadAll(dirs []string, workspacePath string) []Skill {
	seen := map[string]bool{}
	var out []Skill
	for _, rawDir := range dirs {
		d, skip := resolveScanDir(rawDir, workspacePath)
		if skip {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			logging.Debug("skill scan skip", "dir", d, "err", err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// skip hidden dirs except we already are inside skills root
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := filepath.Join(d, e.Name())
			m, ok := loadManifest(dir, e.Name())
			if !ok {
				continue
			}
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			out = append(out, Skill{Manifest: m, Dir: dir, Source: d})
		}
	}
	return out
}

// resolveScanDir expands ~ and maps relative .skills to workspace.
// Returns skip=true when .skills is listed but no workspace is bound.
func resolveScanDir(raw, workspacePath string) (string, bool) {
	d := strings.TrimSpace(raw)
	if d == "" {
		return "", true
	}
	d = config.ExpandHome(d)
	base := filepath.Base(d)
	// bare ".skills" or "./.skills"
	if d == ".skills" || base == ".skills" && !filepath.IsAbs(raw) && !strings.HasPrefix(raw, "~") {
		if workspacePath == "" {
			return "", true
		}
		return filepath.Join(workspacePath, ".skills"), false
	}
	return d, false
}

func loadManifest(dir, fallbackName string) (Manifest, bool) {
	// 1) MCPX skill.yaml
	if raw, err := os.ReadFile(filepath.Join(dir, "skill.yaml")); err == nil {
		var m Manifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			logging.Warn("skill.yaml parse", "path", dir, "err", err)
			return Manifest{}, false
		}
		m.Format = "yaml"
		normalizeManifest(&m, fallbackName)
		return m, true
	}
	// 2) Agent SKILL.md / skill.md
	for _, name := range []string{"SKILL.md", "skill.md"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		m, ok := parseSkillMD(raw, fallbackName)
		if !ok {
			// still register by directory name as doc skill
			m = Manifest{
				Name:    fallbackName,
				Runtime: "markdown",
				Entry:   name,
				Format:  "skill_md",
			}
		}
		normalizeManifest(&m, fallbackName)
		if m.Format == "" {
			m.Format = "skill_md"
		}
		if m.Runtime == "" {
			m.Runtime = "markdown"
		}
		if m.Entry == "" {
			m.Entry = name
		}
		return m, true
	}
	return Manifest{}, false
}

func normalizeManifest(m *Manifest, fallbackName string) {
	if m.Name == "" {
		m.Name = fallbackName
	}
	if m.Format == "yaml" {
		if m.Entry == "" {
			m.Entry = "main.py"
		}
		if m.Runtime == "" {
			m.Runtime = "python"
		}
	}
}

// parseSkillMD reads optional YAML frontmatter between --- lines.
func parseSkillMD(raw []byte, fallbackName string) (Manifest, bool) {
	text := string(raw)
	if !strings.HasPrefix(strings.TrimSpace(text), "---") {
		return Manifest{Name: fallbackName, Runtime: "markdown", Format: "skill_md"}, true
	}
	// find frontmatter
	rest := strings.TrimSpace(text)
	rest = strings.TrimPrefix(rest, "---")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Manifest{}, false
	}
	fm := strings.TrimSpace(rest[:end])
	var m Manifest
	if err := yaml.Unmarshal([]byte(fm), &m); err != nil {
		logging.Debug("skill.md frontmatter", "err", err)
		return Manifest{Name: fallbackName, Runtime: "markdown", Format: "skill_md"}, true
	}
	m.Format = "skill_md"
	if m.Runtime == "" {
		m.Runtime = "markdown"
	}
	return m, true
}

// Find by name.
func Find(skills []Skill, name string) (Skill, bool) {
	for _, s := range skills {
		if s.Manifest.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// DefaultScanDirs returns the default skill search paths (for logging/docs).
func DefaultScanDirs() []string {
	return []string{
		"~/.mcpx/skills",
		"~/.agents/skills",
		"~/.agent/skills",
		"~/.codex/skills",
		"~/.grok/skills",
		".skills",
	}
}
