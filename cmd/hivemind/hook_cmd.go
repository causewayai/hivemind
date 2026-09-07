package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/causewayai/hivemind/internal/cilog"
)

func runHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: hivemind hook <claude|install|print>")
		return 2
	}
	switch args[0] {
	case "claude":
		return runHookClaude(os.Stdin, os.Stdout)
	case "install":
		return runHookInstall(args[1:])
	case "print":
		fmt.Println(hookSettingsSnippet())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hivemind hook: unknown %q\n", args[0])
		return 2
	}
}

// runHookClaude implements the Claude Code PreToolUse contract: read the
// payload, and if tool_input.command is a gh log fetch, emit a
// hookSpecificOutput.updatedInput rewrite. Any other input: emit nothing,
// exit 0 (Claude Code proceeds with the original command).
func runHookClaude(r io.Reader, w io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hivemind hook] read stdin: %v\n", err)
		return 0
	}
	var payload struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "[hivemind hook] parse: %v\n", err)
		return 0
	}
	if payload.ToolName != "Bash" && payload.ToolName != "bash" {
		return 0
	}
	cmd, _ := payload.ToolInput["command"].(string)
	rewritten, ok := cilog.RewriteGHLogCommand(cmd)
	if !ok {
		return 0
	}
	updated := make(map[string]any, len(payload.ToolInput))
	for k, v := range payload.ToolInput {
		updated[k] = v
	}
	updated["command"] = rewritten

	resp := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecisionReason": "hivemind CI-log cache: served from local cache or fetched+cached once",
			"updatedInput":             updated,
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "[hivemind hook] encode: %v\n", err)
		return 0
	}
	return 0
}
