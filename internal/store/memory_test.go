package store

import "testing"

func TestCreateMemory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	embedding := make([]float32, 8)

	entry, err := s.CreateMemory(CreateMemoryInput{
		Content:    "the build is broken on main",
		Scope:      "session",
		SessionID:  "sess-1",
		Source:     "claude-code",
		SourceType: "harness",
		Tags:       []string{"ci", "urgent"},
		Embedding:  embedding,
	})
	if err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	if entry.ID == "" {
		t.Errorf("entry.ID is empty")
	}
	if entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", entry)
	}

	got, err := s.GetMemory(entry.ID)
	if err != nil {
		t.Fatalf("GetMemory() error = %v", err)
	}
	if got.Content != "the build is broken on main" {
		t.Errorf("Content = %q, want %q", got.Content, "the build is broken on main")
	}
	if len(got.Tags) != 2 {
		t.Errorf("Tags = %v, want 2 tags", got.Tags)
	}
}

func TestListMemories_FilterByScopeAndTag(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	mustCreate := func(scope, sessionID string, tags []string) {
		if _, err := s.CreateMemory(CreateMemoryInput{
			Content: "entry", Scope: scope, SessionID: sessionID,
			Source: "claude-code", SourceType: "harness", Tags: tags,
		}); err != nil {
			t.Fatalf("CreateMemory() error = %v", err)
		}
	}
	mustCreate("session", "sess-1", []string{"ci"})
	mustCreate("session", "sess-2", []string{"ci"})
	mustCreate("user", "", []string{"ci"})
	mustCreate("session", "sess-1", []string{"other"})

	got, err := s.ListMemories(ListFilter{Scope: "session", SessionID: "sess-1", Tags: []string{"ci"}})
	if err != nil {
		t.Fatalf("ListMemories() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListMemories() returned %d entries, want 1", len(got))
	}
}

func TestQuery_HybridSemanticAndTagFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 4)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	near := []float32{0.1, 0.1, 0.1, 0.1}
	far := []float32{9.9, 9.9, 9.9, 9.9}

	if _, err := s.CreateMemory(CreateMemoryInput{
		Content: "relevant, tagged", Scope: "user", Source: "etl-jira", SourceType: "harness",
		Tags: []string{"ci"}, Embedding: near,
	}); err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	if _, err := s.CreateMemory(CreateMemoryInput{
		Content: "relevant, untagged", Scope: "user", Source: "etl-jira", SourceType: "harness",
		Tags: nil, Embedding: near,
	}); err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	if _, err := s.CreateMemory(CreateMemoryInput{
		Content: "tagged, irrelevant", Scope: "user", Source: "etl-jira", SourceType: "harness",
		Tags: []string{"ci"}, Embedding: far,
	}); err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}

	got, err := s.Query(QueryInput{Embedding: near, Tags: []string{"ci"}, TopK: 5})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(got) != 1 || got[0].Content != "relevant, tagged" {
		t.Fatalf("Query() = %+v, want exactly the 'relevant, tagged' entry", got)
	}
}
