package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/causewayai/hivemind/internal/config"
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestVersionString(t *testing.T) {
	version = "1.2.3"
	commit = "abc1234"
	date = "2026-09-05T00:00:00Z"
	defer func() { version, commit, date = "dev", "none", "unknown" }()

	got := versionString()
	want := "hivemindd 1.2.3 (commit abc1234, built 2026-09-05T00:00:00Z)"
	if got != want {
		t.Errorf("versionString() = %q, want %q", got, want)
	}
}

func TestDaemon_WriteThenQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir+"/test.db", 768)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	mcpSrv := buildMCPServer(s, embedding.NewHashProvider(768))
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	ctx := context.Background()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	writeRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_write",
		Arguments: map[string]any{
			"session_id": "sess-1",
			"content":    "the build is broken on main",
			"source":     "test-harness",
			"tags":       []string{"ci"},
		},
	})
	if err != nil || writeRes.IsError {
		t.Fatalf("memory_write call failed: err=%v res=%+v", err, writeRes)
	}

	queryRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_query",
		Arguments: map[string]any{
			"session_id": "sess-1",
			"query":      "build",
			"top_k":      5,
		},
	})
	if err != nil || queryRes.IsError {
		t.Fatalf("memory_query call failed: err=%v res=%+v", err, queryRes)
	}
}

func TestServe_WritesPortFileAndShutsDownCleanly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))
	t.Setenv("HIVEMIND_PORT", "0") // OS-assigned, avoids collisions
	t.Setenv("HIVEMIND_CI_LOG_CLEANUP", "off")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, cfg) }()

	portFile := cfg.PortFilePath()
	var port string
	deadline := time.After(3 * time.Second)
	for port == "" {
		select {
		case <-deadline:
			t.Fatal("port file not written within 3s")
		case <-time.After(20 * time.Millisecond):
		}
		if b, err := os.ReadFile(portFile); err == nil {
			port = strings.TrimSpace(string(b))
		}
	}

	// Daemon is reachable on the advertised port.
	resp, err := http.Post("http://127.0.0.1:"+port+"/", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	if err != nil {
		t.Fatalf("POST to daemon failed: %v", err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve() returned error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve() did not return after context cancel")
	}
	if _, err := os.Stat(portFile); !os.IsNotExist(err) {
		t.Error("port file not cleaned up on shutdown")
	}
}
