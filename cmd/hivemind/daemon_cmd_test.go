package main

import (
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

func TestDaemonStatus_RunningAndNot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))

	if code := runDaemonCmd([]string{"status"}); code == 0 {
		t.Error("status should be non-zero when no daemon is running")
	}

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

	if code := runDaemonCmd([]string{"status"}); code != 0 {
		t.Error("status should be zero when the daemon answers")
	}
}

func TestDaemonStop_NoPidFileSaysNotRunning(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(t.TempDir(), "hivemind.db"))
	if code := runDaemonCmd([]string{"stop"}); code != 0 {
		t.Errorf("stop with no pid file should exit 0, got %d", code)
	}
}

func TestDaemonCmd_UnknownSubcommand(t *testing.T) {
	if code := runDaemonCmd([]string{"frobnicate"}); code != 2 {
		t.Errorf("unknown daemon subcommand should exit 2, got %d", code)
	}
	if code := runDaemonCmd(nil); code != 2 {
		t.Errorf("no daemon subcommand should exit 2, got %d", code)
	}
}
