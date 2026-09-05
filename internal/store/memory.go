package store

import (
	"time"

	"github.com/google/uuid"
)

type MemoryEntry struct {
	ID         string
	Content    string
	Scope      string
	SessionID  string
	Source     string
	SourceType string
	ExternalID string
	Tags       []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateMemoryInput struct {
	Content    string
	Scope      string // "session" or "user"
	SessionID  string // required when Scope == "session"
	Source     string
	SourceType string // "harness" (this plan does not build the "etl" path)
	Tags       []string
	Embedding  []float32
}

func (s *Store) CreateMemory(in CreateMemoryInput) (*MemoryEntry, error) {
	now := time.Now().UTC()
	entry := &MemoryEntry{
		ID:         uuid.NewString(),
		Content:    in.Content,
		Scope:      in.Scope,
		SessionID:  in.SessionID,
		Source:     in.Source,
		SourceType: in.SourceType,
		Tags:       in.Tags,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO memory_entries (id, content, scope, session_id, source, source_type, external_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.Content, entry.Scope, nullable(entry.SessionID), entry.Source, entry.SourceType,
		nil, entry.CreatedAt.Format(time.RFC3339), entry.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	rowID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	for _, tag := range in.Tags {
		if _, err := tx.Exec(`INSERT INTO memory_tags (memory_id, tag) VALUES (?, ?)`, entry.ID, tag); err != nil {
			return nil, err
		}
	}

	if in.Embedding != nil {
		if err := s.insertVectorTx(tx, rowID, in.Embedding); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *Store) GetMemory(id string) (*MemoryEntry, error) {
	row := s.db.QueryRow(
		`SELECT id, content, scope, IFNULL(session_id,''), source, source_type, IFNULL(external_id,''), created_at, updated_at
		 FROM memory_entries WHERE id = ?`, id,
	)

	entry := &MemoryEntry{}
	var createdAt, updatedAt string
	if err := row.Scan(&entry.ID, &entry.Content, &entry.Scope, &entry.SessionID, &entry.Source,
		&entry.SourceType, &entry.ExternalID, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	entry.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	entry.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	rows, err := s.db.Query(`SELECT tag FROM memory_tags WHERE memory_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		entry.Tags = append(entry.Tags, tag)
	}
	return entry, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
