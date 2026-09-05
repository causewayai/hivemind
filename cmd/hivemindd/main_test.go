package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
