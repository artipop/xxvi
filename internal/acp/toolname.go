package acp

import "strings"

// ACP has no field for "which tool is this". A permission request describes the
// call the way a person would read it — a title, a kind, and the raw input the
// agent sent — and that is deliberate: the protocol is about showing somebody
// what is about to happen, not about letting the client re-implement the
// agent's tool list.
//
// The policy (ToolPolicy) is written in tool names, though, so the name is
// recovered in the order it is trustworthy:
//
//  1. what the agent said it was, in _meta — the vendor adapters put the name
//     there on the tool call, though not on the permission request, which is
//     why a call is remembered by its id;
//  2. what the call plainly is, from its ACP kind and the shape of its input —
//     an execute call carrying a command is a shell call whatever the agent
//     calls it, and that is the mapping the policy vocabulary is written in;
//  3. the title, which is all some agents ever send.
//
// The names it yields are Claude's, because that is the vocabulary a policy is
// written in: an entry saying `Bash(git log*)` should mean the same thing to
// codex, whose shell tool is called something else, and to an agent that sends
// no name at all.

// metaToolName digs the tool name out of an ACP _meta bag. Two shapes are
// known: a flat one, and the adapters' namespaced {"claudeCode": {"toolName": …}}.
func metaToolName(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	if name, ok := meta["toolName"].(string); ok && name != "" {
		return name
	}
	for _, v := range meta {
		if inner, ok := v.(map[string]any); ok {
			if name, ok := inner["toolName"].(string); ok && name != "" {
				return name
			}
		}
	}
	return ""
}

// acpToolPrefix is what an adapter prepends when it routes a built-in tool back
// through the client's own file system. The tool is still Read; only the road
// it takes is different, so the policy should not have to know about it.
const acpToolPrefix = "mcp__acp__"

// normalizeToolName strips the adapter's routing prefix.
func normalizeToolName(name string) string {
	return strings.TrimPrefix(name, acpToolPrefix)
}

// inferToolName reads the tool off the call itself when nobody named it. kind is
// the ACP tool kind, input the raw tool input.
func inferToolName(kind string, input any) string {
	fields, _ := input.(map[string]any)
	has := func(name string) bool {
		if fields == nil {
			return false
		}
		_, ok := fields[name]
		return ok
	}
	switch kind {
	case "execute":
		// The one case worth getting right: Bash is the entry a policy narrows
		// with a pattern, and an agent that runs commands is running commands
		// whatever its tool is called.
		if has("command") {
			return "Bash"
		}
	case "read":
		if has("file_path") || has("path") || has("abs_path") {
			return "Read"
		}
	case "edit":
		if has("file_path") || has("path") {
			// Write and Edit differ by whether there is something to replace.
			if has("content") && !has("old_string") {
				return "Write"
			}
			return "Edit"
		}
	case "search":
		if has("pattern") {
			return "Grep"
		}
		if has("glob") {
			return "Glob"
		}
	}
	return ""
}
