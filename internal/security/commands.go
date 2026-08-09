package security

import (
	"regexp"
	"strings"

	"mcpx/internal/config"
)

// Decision is the outcome of command policy matching.
type Decision int

const (
	// Allow executes without human confirmation.
	Allow Decision = iota
	// Confirm requires explicit user semantic confirmation before execute.
	Confirm
	// Deny rejects the command.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// ParseDefault maps config string to Decision. Unknown/empty => Confirm (safe built-in).
func ParseDefault(s string) Decision {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "confirm", "":
		return Confirm
	default:
		return Confirm
	}
}

// MatchCommand decides whether a command is allowed, needs confirmation, or must
// be rejected. deny/allow rules match whole commands or individual && / ;
// segments; pipes, redirections, background operators, and command substitution
// are structurally rejected because they cannot be split safely.
func MatchCommand(rules config.CommandRules, command string) Decision {
	segments, unsafe := commandSegments(command)
	if unsafe {
		return Deny
	}
	needsConfirmation := false
	for _, segment := range segments {
		switch matchSegment(rules, segment) {
		case Deny:
			return Deny
		case Confirm:
			needsConfirmation = true
		}
	}
	if needsConfirmation {
		return Confirm
	}
	return Allow
}

// matchSegment evaluates a single command segment without control operators.
func matchSegment(rules config.CommandRules, segment string) Decision {
	if matchAny(rules.Deny, segment) {
		return Deny
	}
	if matchAny(rules.Confirm, segment) {
		return Confirm
	}
	if matchAny(rules.Allow, segment) {
		return Allow
	}
	if autoAllowReadonlyEnabled(rules) && isReadonlyCommand(segment) {
		return Allow
	}
	decision := ParseDefault(rules.Default)
	return decision
}

// HasUnsafeShellOperator reports active shell syntax that cannot be split into
// independently judged segments: pipes, redirections, background operators,
// newlines, and command substitution. Operators inside quotes or escaped with a
// backslash are treated as literal argument content rather than shell control
// syntax.
func HasUnsafeShellOperator(command string) bool {
	_, unsafe := commandSegments(command)
	return unsafe
}

// commandSegments scans shell control syntax without trying to fully parse a
// shell language. It only needs to distinguish active operators from literal
// characters in quoted/escaped arguments, and split safe && / ; separators.
func commandSegments(command string) ([]string, bool) {
	segments := make([]string, 0, 2)
	start := 0
	var quote byte
	escaped := false
	appendSegment := func(end int) {
		segments = append(segments, strings.TrimSpace(command[start:end]))
	}

	for index := 0; index < len(command); index++ {
		current := command[index]
		if escaped {
			escaped = false
			continue
		}

		switch quote {
		case '\'':
			if current == '\'' {
				quote = 0
			}
			continue
		case '"':
			switch current {
			case '\\':
				escaped = true
			case '"':
				quote = 0
			case '`':
				return nil, true
			case '$':
				if index+1 < len(command) && command[index+1] == '(' {
					return nil, true
				}
			}
			continue
		}

		switch current {
		case '\\':
			escaped = true
		case '\'', '"':
			quote = current
		case '|', '<', '>', '`', '\n', '\r':
			return nil, true
		case '$':
			if index+1 < len(command) && command[index+1] == '(' {
				return nil, true
			}
		case '&':
			if index+1 < len(command) && command[index+1] == '&' {
				appendSegment(index)
				index++
				start = index + 1
				continue
			}
			return nil, true
		case ';':
			appendSegment(index)
			start = index + 1
		}
	}
	appendSegment(len(command))
	return segments, false
}

func autoAllowReadonlyEnabled(rules config.CommandRules) bool {
	return rules.AutoAllowReadonly == nil || *rules.AutoAllowReadonly
}

// isReadonlyCommand reports whether every && / ; segment of the command is
// read-only. A single non-read-only segment falls back to normal policy.
func isReadonlyCommand(command string) bool {
	for _, segment := range strings.Split(command, "&&") {
		for _, part := range strings.Split(segment, ";") {
			if !isReadonlySegment(part) {
				return false
			}
		}
	}
	return true
}

// isReadonlySegment accepts only commands with no side effects and no shell
// metacharacters (pipe, redirect, background, command substitution). Arguments
// that can be expanded by the shell are rejected because this matcher does not
// resolve them against the workspace before execution.
func isReadonlySegment(segment string) bool {
	trimmed := strings.TrimSpace(segment)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "|&><`\n") || strings.Contains(trimmed, "$(") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "ls":
		return allReadonlyArguments(fields[1:])
	case "pwd":
		return len(fields) == 1
	case "cat", "head", "tail":
		return allReadonlyArguments(fields[1:])
	case "git":
		return isReadonlyGit(fields[1:])
	}
	return false
}

// isReadonlyGit accepts git -C <relative-path> <status|diff|log|show> and the
// bare <status|diff|log|show> forms. -C targets must be relative workspace
// paths.
func isReadonlyGit(args []string) bool {
	for len(args) > 0 {
		if args[0] != "-C" {
			break
		}
		if len(args) < 2 {
			return false
		}
		if !safeReadonlyArgument(args[1]) {
			return false
		}
		args = args[2:]
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "status", "diff", "log", "show":
		return allReadonlyArguments(args[1:])
	}
	return false
}

func allReadonlyArguments(args []string) bool {
	for _, arg := range args {
		if !safeReadonlyArgument(arg) {
			return false
		}
	}
	return true
}

func safeReadonlyArgument(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "/") || strings.Contains(arg, "..") {
		return false
	}
	// These characters trigger shell expansion or make the path differ from
	// the literal value reviewed by the policy matcher.
	return !strings.ContainsAny(arg, "$~{}\\*?[]'\"")
}

func matchAny(patterns []string, command string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// MatchFile applies file path policy. Once any list is configured, unmatched
// paths require confirmation, making an allow list behave as a real whitelist.
func MatchFile(rules config.FileRules, path string) Decision {
	if matchAny(rules.Deny, path) {
		return Deny
	}
	if matchAny(rules.Confirm, path) {
		return Confirm
	}
	if matchAny(rules.Allow, path) {
		return Allow
	}
	if len(rules.Allow)+len(rules.Confirm)+len(rules.Deny) > 0 {
		return Confirm
	}
	return Allow
}
