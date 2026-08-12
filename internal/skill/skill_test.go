package skill

import (
	"context"
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

func TestLoadAllRejectsEntryTraversal(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "evil")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte("name: evil\nruntime: python\nentry: ../secret.txt\n"), 0o644)
	skills := LoadAll([]string{root}, "")
	if len(skills) != 0 {
		t.Fatalf("traversal entry must reject the skill: %+v", skills)
	}
}

func TestLoadAllRejectsAbsoluteEntry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "evil")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte("name: evil\nruntime: python\nentry: /etc/passwd\n"), 0o644)
	skills := LoadAll([]string{root}, "")
	if len(skills) != 0 {
		t.Fatalf("absolute entry must reject the skill: %+v", skills)
	}
}

func TestLoadAllAcceptsNestedEntry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte("name: demo\nruntime: python\nentry: scripts/main.py\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "scripts", "main.py"), []byte("print(1)\n"), 0o644)
	skills := LoadAll([]string{root}, "")
	if len(skills) != 1 || skills[0].Manifest.Entry != "scripts/main.py" {
		t.Fatalf("nested entry must load: %+v", skills)
	}
}

func TestResolveEntryRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.md")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	sk := Skill{Manifest: Manifest{Name: "demo", Runtime: "markdown", Entry: "SKILL.md", Format: "skill_md"}, Dir: dir}
	if _, err := ResolveEntry(sk); err == nil {
		t.Fatal("symlink escape must be rejected")
	}
	skills := LoadAll([]string{root}, "")
	if len(skills) != 0 {
		t.Fatalf("symlinked skill must be rejected: %+v", skills)
	}
}

func TestExecuteRejectsEntryTraversal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "secret.md"), []byte("secret\n"), 0o600)
	doc := Skill{Manifest: Manifest{Name: "demo", Runtime: "markdown", Entry: "../secret.md", Format: "skill_md"}, Dir: dir}
	if _, err := Execute(context.Background(), doc, root, nil); err == nil {
		t.Fatal("Execute must reject traversal doc entry")
	}
	execSK := Skill{Manifest: Manifest{Name: "demo", Runtime: "python", Entry: "../secret.py"}, Dir: dir}
	if _, err := Execute(context.Background(), execSK, root, nil); err == nil {
		t.Fatal("Execute must reject traversal executable entry")
	}
}
