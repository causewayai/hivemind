package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCILogs_CacheHitPrintsFromCacheNoGH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))

	s, err := store.Open(filepath.Join(dir, "hivemind.db"), 768)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// Seed a run-summary entry as the daemon would have.
	if _, err := s.CreateMemory(store.CreateMemoryInput{
		Content: "RUN 42 conclusion=failure workflow=CI", Scope: "user",
		Source: cilog.Source, SourceType: "etl",
		ExternalID: cilog.RunExternalID("causewayai/hivemind", "42"),
		Tags:       cilog.Tags("causewayai/hivemind", "42", "CI", "abc123", "fail"),
		Embedding:  make([]float32, 768),
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(768))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(strconv.Itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ghCalled := false
	code := runCILogsWith(context.Background(), cilogsDeps{
		stdout: &out,
		gh:     func(context.Context, ...string) ([]byte, error) { ghCalled = true; return nil, nil },
	}, []string{"run", "view", "42", "-R", "causewayai/hivemind", "--log"})

	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out.String())
	}
	if ghCalled {
		t.Fatal("gh must not be called on a cache hit")
	}
	if !bytes.Contains(out.Bytes(), []byte("RUN 42 conclusion=failure")) {
		t.Fatalf("cache-hit output missing summary; got:\n%s", out.String())
	}
}
