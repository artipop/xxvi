package acp

import "strings"

// ToolPolicy is the list of tool calls a session may make without asking.
// An entry is either a bare tool name — "Read", meaning any call to it — or a
// name with an argument pattern — "Bash(git log*)" — which matches only calls
// whose principal argument fits the pattern.
//
// The distinction matters because the interesting tools are not uniform: Read
// is as safe as its name whatever the path, while Bash covers both `git status`
// and `rm -rf /`. Allowing Bash wholesale is the difference between an agent
// that can look around and one that can do anything, so a policy that cannot
// say "only these commands" ends up either uselessly tight or far too loose.
type ToolPolicy []string

// DefaultPolicy is what an agent may do before anybody configures anything:
// look, and nothing else. A card's session has a person to ask (question.go),
// so a tight default costs a question rather than a failed step — and the
// question is answered once per session, not once per call.
var DefaultPolicy = ToolPolicy{"Read", "Grep", "Glob", "Bash(git status*)", "Bash(git diff*)", "Bash(git log*)"}

// patternArg names the input field a pattern is matched against, per tool. Only
// tools that take one meaningful argument can be narrowed this way.
var patternArg = map[string]string{
	"Bash": "command",
}

// Allows reports whether the call runs unasked. input is the tool's raw input
// as the agent sent it; a nil input can only satisfy bare-name entries.
func (p ToolPolicy) Allows(tool string, input any) bool {
	for _, entry := range p {
		name, pattern, hasPattern := splitPolicyEntry(entry)
		if !strings.EqualFold(name, tool) {
			continue
		}
		if !hasPattern {
			return true
		}
		arg, ok := policyArg(tool, input)
		if !ok {
			// The entry is narrower than what we can check, so it cannot be
			// used to approve this call. Something else may still allow it.
			continue
		}
		if matchPattern(pattern, arg) {
			return true
		}
	}
	return false
}

// splitPolicyEntry parses "Bash(git log*)" into its name and pattern.
func splitPolicyEntry(entry string) (name, pattern string, hasPattern bool) {
	entry = strings.TrimSpace(entry)
	open := strings.Index(entry, "(")
	if open == -1 || !strings.HasSuffix(entry, ")") {
		return entry, "", false
	}
	return strings.TrimSpace(entry[:open]), entry[open+1 : len(entry)-1], true
}

// policyArg pulls the argument a pattern applies to out of the tool input.
func policyArg(tool string, input any) (string, bool) {
	field, ok := patternArg[canonicalToolName(tool)]
	if !ok {
		return "", false
	}
	// A shell command is the one argument agents disagree about the shape of,
	// so it has its own reader.
	if field == "command" {
		return shellCommand(input)
	}
	fields, ok := input.(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := fields[field].(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(value), true
}

// canonicalToolName maps a tool to the spelling patternArg is keyed by, so a
// policy written as "bash(...)" still narrows the Bash tool.
func canonicalToolName(tool string) string {
	for known := range patternArg {
		if strings.EqualFold(known, tool) {
			return known
		}
	}
	return tool
}

// matchPattern is a deliberately small glob: "*" stands for any run of
// characters and everything else is literal. Commands are not paths, so the
// path-aware matchers in the standard library would treat "/" as a boundary
// and refuse to match `git log -- src/foo`.
func matchPattern(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(s, part)
		if idx == -1 {
			return false
		}
		s = s[idx+len(part):]
	}
	return strings.HasSuffix(s, last) && len(s) >= len(last)
}

// shellCommand pulls the command out of a tool input, which agents spell three
// ways: a string (claude), an argv (codex runs everything through a shell, so
// the interesting part is the last element), and a parsed form of its own.
//
// The argv case is why this exists: `["/bin/zsh", "-lc", "git status"]` matched
// against `Bash(git *)` has to be "git status" and not the shell that ran it,
// or every policy pattern would silently stop matching.
func shellCommand(input any) (string, bool) {
	fields, ok := input.(map[string]any)
	if !ok {
		return "", false
	}
	switch cmd := fields["command"].(type) {
	case string:
		return strings.TrimSpace(cmd), true
	case []any:
		if len(cmd) == 0 {
			return "", false
		}
		if last, ok := cmd[len(cmd)-1].(string); ok {
			return strings.TrimSpace(last), true
		}
	}
	return "", false
}
