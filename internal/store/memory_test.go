package store

import "testing"

func TestCreateMemory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

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
