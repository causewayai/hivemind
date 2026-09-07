package mcpserver

import (
	"context"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
)

func TestMemoryWrite_DefaultsToSessionScope(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	srv := New(s, embedding.NewHashProvider(8))

	out, err := srv.handleMemoryWrite(context.Background(), MemoryWriteInput{
		SessionID: "sess-1",
		Content:   "the build is broken on main",
		Source:    "claude-code",
		Tags:      []string{"ci"},
	})
	if err != nil {
		t.Fatalf("handleMemoryWrite() error = %v", err)
	}
	if out.ID == "" {
		t.Errorf("out.ID is empty")
	}

	got, err := s.GetMemory(out.ID)
	if err != nil {
		t.Fatalf("GetMemory() error = %v", err)
	}
	if got.Scope != "session" {
		t.Errorf("Scope = %q, want %q", got.Scope, "session")
	}
	if got.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "sess-1")
	}
}

func openMCPTestStore(t *testing.T) *store.Store {
	t.Helper()
	const dim = 8
	s, err := store.Open(t.TempDir()+"/test.db", dim)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMemoryWrite_ExplicitUserScopeEtlExternalID(t *testing.T) {
	s := openMCPTestStore(t)
	srv := New(s, embedding.NewHashProvider(8))

	out, err := srv.handleMemoryWrite(context.Background(), MemoryWriteInput{
		Content: "run summary", Source: "github-actions",
		Scope: "user", SourceType: "etl", ExternalID: "o/r#1",
	})
	if err != nil {
		t.Fatalf("handleMemoryWrite() error = %v", err)
	}
	got, _ := s.GetMemory(out.ID)
	if got.Scope != "user" || got.SourceType != "etl" || got.ExternalID != "o/r#1" {
		t.Fatalf("stored entry = %+v, want scope=user source_type=etl external_id=o/r#1", got)
	}
}

func TestMemoryWrite_UpsertReturnsSameID(t *testing.T) {
	s := openMCPTestStore(t)
	srv := New(s, embedding.NewHashProvider(8))
	in := MemoryWriteInput{Content: "a", Source: "github-actions", Scope: "user", SourceType: "etl", ExternalID: "o/r#1"}

	first, err := srv.handleMemoryWrite(context.Background(), in)
	if err != nil {
		t.Fatalf("first write error = %v", err)
	}
	in.Content = "b"
	second, err := srv.handleMemoryWrite(context.Background(), in)
	if err != nil {
		t.Fatalf("second write error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent upsert must return same ID: %q vs %q", first.ID, second.ID)
	}
}

func TestMemoryWrite_RejectsInvalidScope(t *testing.T) {
	s := openMCPTestStore(t)
	srv := New(s, embedding.NewHashProvider(8))
	_, err := srv.handleMemoryWrite(context.Background(), MemoryWriteInput{
		Content: "x", Source: "h", Scope: "team",
	})
	if err == nil {
		t.Fatal("scope=team must be rejected")
	}
}
