package main

import "testing"

func TestDaemonOlderThanCLI(t *testing.T) {
	cases := []struct {
		cli, daemon string
		older       bool
	}{
		{"1.2.0", "1.1.9", true},
		{"1.2.0", "1.2.0", false},
		{"1.2.0", "1.3.0", false},
		{"dev", "1.0.0", false}, // local dev: never refuse
		{"1.0.0", "dev", false}, // daemon dev build: never refuse
		{"1.2.0", "garbage", false},
		{"v1.2.0", "v1.1.0", true},    // leading v tolerated
		{"1.2.0", "1.2.0-rc1", false}, // pre-release suffix stripped -> equal -> not older
	}
	for _, c := range cases {
		if got := daemonOlderThanCLI(c.cli, c.daemon); got != c.older {
			t.Errorf("daemonOlderThanCLI(%q,%q) = %v, want %v", c.cli, c.daemon, got, c.older)
		}
	}
}
