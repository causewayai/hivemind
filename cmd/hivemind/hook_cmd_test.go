package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHookClaude_RewritesLogFetch(t *testing.T) {
	in := `{"tool_name":"Bash","tool_input":{"command":"gh run view 42 --log -R o/r","description":"logs"}}`
	var out bytes.Buffer
	code := runHookClaude(strings.NewReader(in), &out)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var resp struct {
		HookSpecificOutput struct {
			UpdatedInput map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out.String())
	}
	if resp.HookSpecificOutput.UpdatedInput["command"] != "hivemind ci-logs run view 42 --log -R o/r" {
		t.Fatalf("wrong rewrite: %v", resp.HookSpecificOutput.UpdatedInput)
	}
	if resp.HookSpecificOutput.UpdatedInput["description"] != "logs" {
		t.Fatalf("other tool_input fields must be preserved: %v", resp.HookSpecificOutput.UpdatedInput)
	}
}

func TestHookClaude_PassthroughEmitsNothing(t *testing.T) {
	var out bytes.Buffer
	code := runHookClaude(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"gh run list"}}`), &out)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("passthrough must emit nothing; code=%d out=%q", code, out.String())
	}
}
