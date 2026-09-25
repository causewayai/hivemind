package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEnsureDaemon_ReturnsExistingWithoutSpawning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))
	// Point the daemon-binary resolver at a path that would fail if invoked.
	t.Setenv("HIVEMIND_DAEMON_BIN", filepath.Join(dir, "does-not-exist"))

	s, err := store.Open(filepath.Join(dir, "hivemind.db"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(8))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(strconv.Itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon() error = %v", err)
	}
	_ = sess.Close()
}

// Cold start: neither the daemon nor its runtime dir exists. ensureDaemon must
// create the runtime dir before taking the lock file, then fail at the spawn
// step (no real daemon binary) — NOT with a "no such file or directory" on the
// lock. Regression test for the E2E-caught bug.
func TestEnsureDaemon_CreatesRuntimeDirBeforeLock(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "does-not-exist-yet")
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(runtime, "hivemind.db"))
	t.Setenv("HIVEMIND_DAEMON_BIN", filepath.Join(dir, "no-such-binary"))

	_, err := ensureDaemon(context.Background())
	if err == nil {
		t.Fatal("expected an error (no daemon binary), got nil")
	}
	if strings.Contains(err.Error(), "daemon lock:") {
		t.Fatalf("failed at the lock step, runtime dir not created first: %v", err)
	}
	if _, statErr := os.Stat(runtime); statErr != nil {
		t.Errorf("runtime dir not created: %v", statErr)
	}
}

func TestDaemonBinary_PrefersEnvOverride(t *testing.T) {
	t.Setenv("HIVEMIND_DAEMON_BIN", "/custom/hivemindd")
	got, err := daemonBinary()
	if err != nil {
		t.Fatalf("daemonBinary() error = %v", err)
	}
	if got != "/custom/hivemindd" {
		t.Errorf("daemonBinary() = %q, want /custom/hivemindd", got)
	}
}
