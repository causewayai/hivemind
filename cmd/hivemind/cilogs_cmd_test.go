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
	"strings"
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

func TestCILogs_CacheMissFetchesAndPopulates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))
	t.Setenv("HIVEMIND_CI_LOG_DIR", filepath.Join(dir, "ci-logs"))

	s, err := store.Open(filepath.Join(dir, "hivemind.db"), 768)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(768))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(strconv.Itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}

	logText := "build\t2026-09-06T00:00:02Z ##[error]boom\n"
	metaJSON := `{"workflowName":"CI","conclusion":"failure","headSha":"abc","headBranch":"main","event":"push","jobs":[{"databaseId":7,"name":"build","conclusion":"failure"}]}`

	var out bytes.Buffer
	code := runCILogsWith(context.Background(), cilogsDeps{
		stdout: &out,
		gh: func(_ context.Context, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "--json"):
				return []byte(metaJSON), nil
			case strings.Contains(joined, "--log"):
				return []byte(logText), nil
			case strings.Contains(joined, "repo view"):
				return []byte("causewayai/hivemind\n"), nil
			}
			return nil, nil
		},
	}, []string{"run", "view", "42", "-R", "causewayai/hivemind", "--log"})

	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "##[error]boom") {
		t.Errorf("stdout should echo the gh --log output; got:\n%s", out.String())
	}
	runLog := cilog.RunLogPath(filepath.Join(dir, "ci-logs"), "causewayai", "hivemind", "42")
	if _, err := os.Stat(runLog); err != nil {
		t.Errorf("run.log not written: %v", err)
	}
	sum, _ := s.GetMemoryByExternalID(cilog.Source, cilog.RunExternalID("causewayai/hivemind", "42"), "user")
	if sum == nil {
		t.Error("run-summary entry not written")
	}
	fail, _ := s.GetMemoryByExternalID(cilog.Source, cilog.JobExternalID("causewayai/hivemind", "42", "7"), "user")
	if fail == nil {
		t.Error("job-failure entry not written")
	}
}
