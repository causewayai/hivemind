//go:build !windows

package main

import "testing"

func TestDaemonStopIsGraceful_Unix(t *testing.T) {
	if !daemonStopIsGraceful {
		t.Error("unix builds stop the daemon gracefully via SIGTERM; the daemon removes its own port/pid files")
	}
}
