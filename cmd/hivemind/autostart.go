package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func lockFilePath() string { return filepath.Join(runtimeDir(), "daemon.lock") }
func logFilePath() string  { return filepath.Join(runtimeDir(), "logs", "daemon.log") }

// ensureDaemon returns an MCP session to a running hivemindd, starting one if
// necessary. Concurrent invocations serialize on a lock file so only one
// spawns.
func ensureDaemon(ctx context.Context) (*mcp.ClientSession, error) {
	if sess, err := dialDaemon(ctx); err == nil {
		return sess, nil
	}

	// The daemon creates the runtime dir itself, but we need it now for the
	// lock file — before any daemon exists.
	if err := os.MkdirAll(runtimeDir(), 0o755); err != nil {
		return nil, fmt.Errorf("runtime dir: %w", err)
	}

	unlock, err := acquireLock(lockFilePath())
	if err != nil {
		return nil, fmt.Errorf("daemon lock: %w", err)
	}
	defer unlock()

	// Someone may have started it while we waited for the lock.
	if sess, err := dialDaemon(ctx); err == nil {
		return sess, nil
	}

	bin, err := daemonBinary()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logFilePath()), 0o755); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(logFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() { _ = logf.Close() }()

	cmd := exec.Command(bin)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachAttrs() // platform-specific (flock_*.go)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hivemindd: %w", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess, err := dialDaemon(ctx); err == nil {
			return sess, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	tail, _ := os.ReadFile(logFilePath())
	return nil, fmt.Errorf("hivemindd did not become ready within 5s; recent log:\n%s", lastLines(string(tail), 20))
}

// daemonBinary resolves the hivemindd executable: $HIVEMIND_DAEMON_BIN, else a
// sibling of this binary, else PATH.
func daemonBinary() (string, error) {
	if v := os.Getenv("HIVEMIND_DAEMON_BIN"); v != "" {
		return v, nil
	}
	if self, err := os.Executable(); err == nil {
		sib := filepath.Join(filepath.Dir(self), daemonExeName())
		if _, err := os.Stat(sib); err == nil {
			return sib, nil
		}
	}
	if p, err := exec.LookPath("hivemindd"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("hivemindd not found (set HIVEMIND_DAEMON_BIN, or install it next to hivemind / on PATH)")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
