package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDialDaemon_ConnectsUsingPortFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))

	s, err := store.Open(filepath.Join(dir, "hivemind.db"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(8))
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(h) // 127.0.0.1:<port>
	defer ts.Close()

	port := ts.Listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(fmt.Sprint(port)), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := dialDaemon(context.Background())
	if err != nil {
		t.Fatalf("dialDaemon() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_scopes", Arguments: map[string]any{},
	})
	if err != nil || res.IsError {
		t.Fatalf("list_scopes via dialed session failed: err=%v res=%+v", err, res)
	}
}

func TestDaemonRunning_FalseWhenNoPortFile(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(t.TempDir(), "hivemind.db"))
	if daemonRunning(context.Background()) {
		t.Error("daemonRunning() should be false when there is no port file")
	}
}
