package store

import "testing"

func openTestStore(t *testing.T, dim int) *Store {
	t.Helper()
	s, err := Open(t.TempDir()+"/test.db", dim)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateMemory(t *testing.T) {
	s := openTestStore(t, 8)

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
	s := openTestStore(t, 8)

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
	s := openTestStore(t, 4)

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

func TestCreateMemory_ExternalIDRoundTrips(t *testing.T) {
	s := openTestStore(t, 8)
	entry, err := s.CreateMemory(CreateMemoryInput{
		Content: "run summary", Scope: "user", Source: "github-actions",
		SourceType: "etl", ExternalID: "o/r#42", Embedding: make([]float32, 8),
	})
	if err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	got, err := s.GetMemory(entry.ID)
	if err != nil {
		t.Fatalf("GetMemory() error = %v", err)
	}
	if got.ExternalID != "o/r#42" {
		t.Errorf("ExternalID = %q, want %q", got.ExternalID, "o/r#42")
	}
}

func TestCreateMemory_UpsertIsIdempotent(t *testing.T) {
	s := openTestStore(t, 8)
	in := CreateMemoryInput{
		Content: "first", Scope: "user", Source: "github-actions",
		SourceType: "etl", ExternalID: "o/r#42", Tags: []string{"run_id:42"},
		Embedding: make([]float32, 8),
	}
	first, err := s.CreateMemory(in)
	if err != nil {
		t.Fatalf("first CreateMemory() error = %v", err)
	}

	in.Content = "second — should be ignored"
	second, err := s.CreateMemory(in)
	if err != nil {
		t.Fatalf("second CreateMemory() error = %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("upsert returned a new ID %q, want existing %q", second.ID, first.ID)
	}

	got, _ := s.GetMemory(first.ID)
	if got.Content != "first" {
		t.Errorf("content mutated to %q; upsert must be insert-if-absent only", got.Content)
	}
	all, _ := s.ListMemories(ListFilter{Source: "github-actions"})
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 row after 2 idempotent upserts, got %d", len(all))
	}
	if len(got.Tags) != 1 {
		t.Errorf("tags duplicated on upsert: %v", got.Tags)
	}
}

func TestCreateMemory_SameExternalIDDifferentScope(t *testing.T) {
	s := openTestStore(t, 8)
	mk := func(scope, sess string) {
		if _, err := s.CreateMemory(CreateMemoryInput{
			Content: "x", Scope: scope, SessionID: sess, Source: "github-actions",
			SourceType: "etl", ExternalID: "o/r#1", Embedding: make([]float32, 8),
		}); err != nil {
			t.Fatalf("CreateMemory(%s) error = %v", scope, err)
		}
	}
	mk("user", "")
	mk("session", "sess-1")
	all, _ := s.ListMemories(ListFilter{Source: "github-actions"})
	if len(all) != 2 {
		t.Fatalf("composite key must allow same external_id in two scopes; got %d rows", len(all))
	}
}

func TestGetMemoryByExternalID(t *testing.T) {
	s := openTestStore(t, 8)
	if _, err := s.CreateMemory(CreateMemoryInput{
		Content: "x", Scope: "user", Source: "github-actions", SourceType: "etl",
		ExternalID: "o/r#7", Embedding: make([]float32, 8),
	}); err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	got, err := s.GetMemoryByExternalID("github-actions", "o/r#7", "user")
	if err != nil {
		t.Fatalf("GetMemoryByExternalID() error = %v", err)
	}
	if got == nil || got.Content != "x" {
		t.Fatalf("GetMemoryByExternalID() = %+v, want the entry", got)
	}
	miss, err := s.GetMemoryByExternalID("github-actions", "nope", "user")
	if err != nil {
		t.Fatalf("miss should not error, got %v", err)
	}
	if miss != nil {
		t.Fatalf("expected nil for no match, got %+v", miss)
	}
}

func TestDeleteMemory_RemovesEntryTagsAndVector(t *testing.T) {
	s := openTestStore(t, 4)
	e, err := s.CreateMemory(CreateMemoryInput{
		Content: "x", Scope: "user", Source: "github-actions", SourceType: "etl",
		ExternalID: "o/r#1", Tags: []string{"log_path:/tmp/a.log"},
		Embedding: []float32{0.1, 0.1, 0.1, 0.1},
	})
	if err != nil {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	if err := s.DeleteMemory(e.ID); err != nil {
		t.Fatalf("DeleteMemory() error = %v", err)
	}
	if got, _ := s.GetMemory(e.ID); got != nil {
		t.Fatal("entry still present after DeleteMemory")
	}
	var tagCount int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memory_tags WHERE memory_id = ?`, e.ID).Scan(&tagCount)
	if tagCount != 0 {
		t.Errorf("tags not cascaded: %d remain", tagCount)
	}
}

func TestListMemories_FilterByExternalID(t *testing.T) {
	s := openTestStore(t, 8)
	mk := func(ext string) {
		if _, err := s.CreateMemory(CreateMemoryInput{
			Content: "x", Scope: "user", Source: "github-actions", SourceType: "etl",
			ExternalID: ext, Embedding: make([]float32, 8),
		}); err != nil {
			t.Fatalf("CreateMemory(%s) error = %v", ext, err)
		}
	}
	mk("o/r#1")
	mk("o/r#2")
	mk("o/r#2#99") // job-level entry for run 2

	got, err := s.ListMemories(ListFilter{ExternalID: "o/r#2"})
	if err != nil {
		t.Fatalf("ListMemories() error = %v", err)
	}
	if len(got) != 1 || got[0].ExternalID != "o/r#2" {
		t.Fatalf("ListMemories(ExternalID=o/r#2) = %+v, want exactly the run-2 summary", got)
	}
}
