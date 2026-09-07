package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const hookCommand = "hivemind hook claude"

func hookSettingsSnippet() string {
	return `{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "hivemind hook claude" } ] }
    ]
  }
}`
}

func defaultClaudeSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func runHookInstall(args []string) int {
	fs := flag.NewFlagSet("hook install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("settings", defaultClaudeSettingsPath(), "path to Claude Code settings.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "hivemind: cannot locate ~/.claude/settings.json; pass --settings")
		return 1
	}

	root := map[string]any{}
	if b, err := os.ReadFile(*path); err == nil {
		if err := json.Unmarshal(b, &root); err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: %s is not valid JSON: %v\n", *path, err)
			return 1
		}
	}

	if hookAlreadyInstalled(root) {
		fmt.Printf("hivemind hook already present in %s\n", *path)
		return 0
	}
	insertHook(root)

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*path, append(out, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	fmt.Printf("installed hivemind PreToolUse hook into %s\n", *path)
	return 0
}

func hookAlreadyInstalled(root map[string]any) bool {
	hooks, _ := root["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	for _, e := range pre {
		em, _ := e.(map[string]any)
		hs, _ := em["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if hm["command"] == hookCommand {
				return true
			}
		}
	}
	return false
}

func insertHook(root map[string]any) {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	pre, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(pre, map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": hookCommand},
		},
	})
}
