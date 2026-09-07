package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
