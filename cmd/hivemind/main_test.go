package main

import "testing"

func TestVersionString(t *testing.T) {
	version, commit, date = "1.2.3", "abc1234", "2026-09-06T00:00:00Z"
	defer func() { version, commit, date = "dev", "none", "unknown" }()
	if got, want := versionString(), "hivemind 1.2.3 (commit abc1234, built 2026-09-06T00:00:00Z)"; got != want {
		t.Errorf("versionString() = %q, want %q", got, want)
	}
}

func TestDispatch_UnknownSubcommand(t *testing.T) {
	if code := dispatch([]string{"bogus"}); code == 0 {
		t.Error("unknown subcommand should exit non-zero")
	}
}
