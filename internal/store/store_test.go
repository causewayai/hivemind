package store

import "testing"

func TestOpen_CreatesSchema(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 768)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	var name string
	row := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_entries'`)
	if err := row.Scan(&name); err != nil {
		t.Fatalf("memory_entries table missing: %v", err)
	}

	row = s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_tags'`)
	if err := row.Scan(&name); err != nil {
		t.Fatalf("memory_tags table missing: %v", err)
	}
}
