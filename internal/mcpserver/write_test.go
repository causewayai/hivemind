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
	defer s.Close()

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
