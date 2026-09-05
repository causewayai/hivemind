package mcpserver

import (
	"context"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
)

func TestMemoryQuery_SessionIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer s.Close()

	provider := embedding.NewHashProvider(8)
	srv := New(s, provider)
	ctx := context.Background()

	// Query embeds the free-text query itself ("secret") via the same
	// HashProvider, which is explicitly non-semantic (see
	// internal/embedding/hash.go) — it does not place related strings near
	// each other in vector space. This test is about session/scope
	// isolation, not semantic recall, so fixtures pin an explicit embedding
	// matching the query's so semantic distance never gates inclusion here;
	// Task 8 (internal/store/memory_test.go) covers semantic relevance
	// filtering with vectors designed for that purpose.
	queryVec, err := provider.Embed("secret")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if _, err := srv.handleMemoryWrite(ctx, MemoryWriteInput{SessionID: "sess-A", Content: "A's secret", Source: "harness-a", Embedding: queryVec}); err != nil {
		t.Fatalf("write A error = %v", err)
	}
	if _, err := srv.handleMemoryWrite(ctx, MemoryWriteInput{SessionID: "sess-B", Content: "B's secret", Source: "harness-b", Embedding: queryVec}); err != nil {
		t.Fatalf("write B error = %v", err)
	}
	if _, err := s.CreateMemory(store.CreateMemoryInput{
		Content: "shared team fact", Scope: "user", Source: "seed", SourceType: "harness",
		Embedding: queryVec,
	}); err != nil {
		t.Fatalf("seed user-scope entry error = %v", err)
	}

	out, err := srv.handleMemoryQuery(ctx, MemoryQueryInput{SessionID: "sess-A", Query: "secret", TopK: 10})
	if err != nil {
		t.Fatalf("handleMemoryQuery() error = %v", err)
	}

	var sawOwn, sawOthers, sawShared bool
	for _, e := range out.Results {
		switch e.Content {
		case "A's secret":
			sawOwn = true
		case "B's secret":
			sawOthers = true
		case "shared team fact":
			sawShared = true
		}
	}
	if !sawOwn {
		t.Errorf("expected to see own session-scope entry")
	}
	if sawOthers {
		t.Errorf("must not see another session's session-scope entry")
	}
	if !sawShared {
		t.Errorf("expected to see user-scope entry")
	}
}
