package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHookInstall_IdempotentAndPreservesKeys(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet","hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"other"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runHookInstall([]string{"--settings", settings}); code != 0 {
		t.Fatalf("install exit = %d", code)
	}
	if code := runHookInstall([]string{"--settings", settings}); code != 0 {
		t.Fatalf("second install exit = %d", code)
	}

	m := readJSON(t, settings)
	if m["model"] != "sonnet" {
		t.Error("unrelated key 'model' was dropped")
	}
	pre := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	hivemindEntries := 0
	for _, e := range pre {
		hooks := e.(map[string]any)["hooks"].([]any)
		for _, h := range hooks {
			if h.(map[string]any)["command"] == "hivemind hook claude" {
				hivemindEntries++
			}
		}
	}
	if hivemindEntries != 1 {
		t.Fatalf("want exactly 1 hivemind hook entry after 2 installs, got %d", hivemindEntries)
	}
	if len(pre) < 2 {
		t.Error("pre-existing Read matcher entry was lost")
	}
}

func TestHookInstall_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "nested", "settings.json")
	if code := runHookInstall([]string{"--settings", settings}); code != 0 {
		t.Fatalf("install exit = %d", code)
	}
	m := readJSON(t, settings)
	pre := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("want 1 entry in a fresh file, got %d", len(pre))
	}
}

func TestHookSettingsSnippet_IsValidJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(hookSettingsSnippet()), &v); err != nil {
		t.Fatalf("snippet is not valid JSON: %v\n%s", err, hookSettingsSnippet())
	}
}
