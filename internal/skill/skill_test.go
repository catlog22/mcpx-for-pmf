package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllYAML(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte("name: demo\nruntime: python\nentry: main.py\narguments_schema:\n  type: object\n  required: [query]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main.py"), []byte("print(1)\n"), 0o644)
	skills := LoadAll([]string{root}, "")
	if len(skills) != 1 || skills[0].Manifest.Name != "demo" {
		t.Fatalf("%+v", skills)
	}
	if skills[0].Manifest.ArgumentsSchema["type"] != "object" {
		t.Fatalf("arguments schema: %+v", skills[0].Manifest.ArgumentsSchema)
	}
}

func TestLoadAllSkillMD(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "brainstorming")
	_ = os.MkdirAll(dir, 0o755)
	body := "---\nname: brainstorming\ndescription: design\n---\n\n# Hello\n"
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644)
	skills := LoadAll([]string{root}, "")
	if len(skills) != 1 {
		t.Fatalf("count %d %+v", len(skills), skills)
	}
	if skills[0].Manifest.Name != "brainstorming" || skills[0].Manifest.Runtime != "markdown" {
		t.Fatalf("%+v", skills[0].Manifest)
	}
	if skills[0].Manifest.Format != "skill_md" {
		t.Fatalf("format %s", skills[0].Manifest.Format)
	}
}

func TestLoadAgentsSkillsDir(t *testing.T) {
	// Integration-ish: if user has ~/.agents/skills, ensure we find at least one.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	agents := filepath.Join(home, ".agents", "skills")
	if _, err := os.Stat(agents); err != nil {
		t.Skip("no ~/.agents/skills")
	}
	skills := LoadAll([]string{"~/.agents/skills"}, "")
	if len(skills) == 0 {
		t.Fatal("expected to discover SKILL.md packages under ~/.agents/skills")
	}
	t.Logf("found %d skills, first=%s", len(skills), skills[0].Manifest.Name)
}
