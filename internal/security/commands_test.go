package security

import (
	"strings"
	"testing"

	"mcpx/internal/config"
)

func TestMatchCommandDenyPrefixBlocksCommand(t *testing.T) {
	rules := config.CommandRules{Deny: []string{`^rm\b`, `^git push`}}
	for _, command := range []string{
		"rm -rf ./x",
		"git push origin main",
		"git status && rm -rf ./x",
		"git status; git push",
		"ls -la && rm x",
	} {
		if got := MatchCommand(rules, command); got != Deny {
			t.Errorf("%q: got %s, want deny", command, got)
		}
	}
	for _, command := range []string{
		"git status",
		"git status && git diff",
		"go test ./...",
		"echo hi",
	} {
		want := Confirm
		if strings.HasPrefix(command, "git status") {
			want = Allow
		}
		if got := MatchCommand(rules, command); got != want {
			t.Errorf("%q: got %s, want %s", command, got, want)
		}
	}
}

func TestMatchCommandDefaultsToConfirm(t *testing.T) {
	rules := config.CommandRules{}
	for _, command := range []string{
		"rm -rf ./x", "git push origin main", "echo hi", "npm test",
	} {
		if got := MatchCommand(rules, command); got != Confirm {
			t.Errorf("%q: got %s, want confirm", command, got)
		}
	}
	for _, command := range []string{"git status", "git -C sub status --short", "ls -la", "cat internal/a.go"} {
		if got := MatchCommand(rules, command); got != Allow {
			t.Errorf("readonly %q: got %s, want allow", command, got)
		}
	}
}

func TestDefaultCommandPolicyAllowsUnmatchedCommands(t *testing.T) {
	rules := config.DefaultConfig().Security.Commands
	for _, command := range []string{"go test ./...", "rm -rf ./x", "echo hi"} {
		if got := MatchCommand(rules, command); got != Allow {
			t.Errorf("%q: got %s, want allow", command, got)
		}
	}
	if got := MatchCommand(rules, "git push origin main"); got != Confirm {
		t.Fatalf("explicit confirm rule got %s, want confirm", got)
	}
}

func TestMatchCommandRejectsUnsafeOperators(t *testing.T) {
	// Active pipes, redirections, background operators, and command substitution
	// cannot be split into independently judged segments and are rejected.
	rules := config.CommandRules{}
	for _, command := range []string{
		"ls | sh",
		"ls > out.txt",
		"cat < in.txt",
		"ls $(dangerous)",
		"echo \"$(dangerous)\"",
		"ls `id`",
		"echo \"`id`\"",
		"sleep 5 &",
		"ls\nrm -rf x",
	} {
		if got := MatchCommand(rules, command); got != Deny {
			t.Errorf("%q: got %s, want deny", command, got)
		}
	}
}

func TestMatchCommandTreatsQuotedAndEscapedOperatorsAsLiterals(t *testing.T) {
	rules := config.DefaultConfig().Security.Commands
	for _, command := range []string{
		`grep -R -n "changed_lines\|diff_summary\|Created\|Updated" internal cmd`,
		`printf '%s\n' 'left|right'`,
		`printf "%s" "left > right"`,
		`printf "%s" '$(not-a-substitution)'`,
		`printf "%s" '\` + "`" + `not-a-substitution\` + "`" + `'`,
		`printf foo \| bar`,
		`printf "text; rm -rf ignored"`,
		`printf "text && rm -rf ignored"`,
	} {
		if got := MatchCommand(rules, command); got != Allow {
			t.Errorf("literal operator command %q: got %s, want allow", command, got)
		}
		if HasUnsafeShellOperator(command) {
			t.Errorf("literal operator command %q reported unsafe", command)
		}
	}
}

func TestMatchCommandSegmentsRespectDenyAndAllowLists(t *testing.T) {
	rules := config.CommandRules{Allow: []string{`^go\b`}, Deny: []string{`^rm\b`}}
	if got := MatchCommand(rules, "go test && rm -rf x"); got != Deny {
		t.Fatalf("deny segment must reject the whole command, got %s", got)
	}
	if got := MatchCommand(rules, "go build && go vet"); got != Allow {
		t.Fatalf("allowed segments must run, got %s", got)
	}
	if got := MatchCommand(rules, "go build"); got != Allow {
		t.Fatalf("allow list got %s", got)
	}
}

func TestMatchCommandConfirmRulesReturnConfirm(t *testing.T) {
	rules := config.CommandRules{Confirm: []string{`^git push`, `^docker`}}
	for _, command := range []string{"git push origin main", "docker build .", "echo hi"} {
		if got := MatchCommand(rules, command); got != Confirm {
			t.Errorf("%q: got %s, want confirm", command, got)
		}
	}
}

func TestMatchCommandRejectsReadonlyShellExpansion(t *testing.T) {
	rules := config.CommandRules{}
	for _, command := range []string{
		"cat $HOME/.ssh/id_rsa", "cat ~/.mcpx/config.yaml", "ls ${HOME}",
		"git -C \\${WORKSPACE} status", "cat secrets/*.txt",
	} {
		if got := MatchCommand(rules, command); got != Confirm {
			t.Errorf("%q: got %s, want confirm after readonly rejection", command, got)
		}
	}
}

func TestMatchCommandExplicitDenyBeatsAllow(t *testing.T) {
	rules := config.CommandRules{Allow: []string{`^git status`}, Deny: []string{`^git status`}}
	if got := MatchCommand(rules, "git status"); got != Deny {
		t.Fatalf("explicit deny got %s", got)
	}
}

func TestMatchCommandAutoAllowReadonlyWithDenyDefault(t *testing.T) {
	// The read-only whitelist still opens read-only commands under a deny
	// default, and can be switched off.
	rules := config.CommandRules{Default: "deny"}
	for _, command := range []string{"git status", "git -C sub status --short", "ls"} {
		if got := MatchCommand(rules, command); got != Allow {
			t.Errorf("%q: got %s, want allow", command, got)
		}
	}
	if got := MatchCommand(rules, "rm -rf ./x"); got != Deny {
		t.Fatalf("non-readonly command got %s, want deny", got)
	}
	disabled := false
	rules = config.CommandRules{Default: "deny", AutoAllowReadonly: &disabled}
	if got := MatchCommand(rules, "git status"); got != Deny {
		t.Fatalf("disabled auto allow got %s, want deny", got)
	}
}

func TestMatchFileConfiguredRulesDefaultToConfirm(t *testing.T) {
	rules := config.FileRules{
		Allow: []string{`^src/`},
		Deny:  []string{`(^|/)\.env$`},
	}
	if got := MatchFile(rules, ".env"); got != Deny {
		t.Fatalf("deny got %s", got)
	}
	if got := MatchFile(rules, "src/main.go"); got != Allow {
		t.Fatalf("allow got %s", got)
	}
	if got := MatchFile(rules, "README.md"); got != Confirm {
		t.Fatalf("unmatched got %s", got)
	}
}

func TestMatchCommandAutoAllowReadonly(t *testing.T) {
	rules := config.CommandRules{}
	for _, command := range []string{
		"git status",
		"git -C fanyi-cloud status --short && git -C fanyi-cloud-ui status --short",
		"git status; git diff",
		"git log --oneline",
		"git show HEAD",
		"ls",
		"ls -la",
		"cat internal/server/tools.go",
		"head -5 go.mod",
		"tail -5 go.mod",
		"pwd",
	} {
		if got := MatchCommand(rules, command); got != Allow {
			t.Errorf("%q: got %s, want allow", command, got)
		}
	}
}

func TestParseDefault(t *testing.T) {
	if ParseDefault("") != Confirm || ParseDefault("confirm") != Confirm {
		t.Fatal("confirm")
	}
	if ParseDefault("allow") != Allow {
		t.Fatal("allow")
	}
	if ParseDefault("deny") != Deny {
		t.Fatal("deny")
	}
}
