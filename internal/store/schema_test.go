package store

import (
	"strings"
	"testing"
)

func TestSchema_PartialUniqueIndexOnExternalID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	insert := func(id, source, extID, scope string) error {
		_, err := s.db.Exec(
			`INSERT INTO memory_entries (id, content, scope, source, source_type, external_id, created_at, updated_at)
			 VALUES (?, 'x', ?, ?, 'etl', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, scope, source, nullable(extID),
		)
		return err
	}

	if err := insert("a", "github-actions", "o/r#1", "user"); err != nil {
		t.Fatalf("first insert error = %v", err)
	}
	// Same (source, external_id, scope) -> must violate the unique index.
	if err := insert("b", "github-actions", "o/r#1", "user"); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate (source, external_id, scope) should fail with UNIQUE, got %v", err)
	}
	// Same external_id, different scope -> allowed (composite key).
	if err := insert("c", "github-actions", "o/r#1", "session"); err != nil {
		t.Fatalf("same external_id different scope should be allowed, got %v", err)
	}
	// Same external_id + scope, different source -> allowed (source is part of the composite key).
	if err := insert("f", "gitlab-ci", "o/r#1", "user"); err != nil {
		t.Fatalf("same external_id+scope, different source should be allowed, got %v", err)
	}
	// Two NULL external_id rows with same source/scope -> allowed (partial index).
	if err := insert("d", "seed", "", "user"); err != nil {
		t.Fatalf("first NULL external_id insert error = %v", err)
	}
	if err := insert("e", "seed", "", "user"); err != nil {
		t.Fatalf("second NULL external_id insert should be allowed by partial index, got %v", err)
	}
}
