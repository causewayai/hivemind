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

type ListFilter struct {
	Scope     string // "session" or "user"; empty means both
	SessionID string // required when filtering scope == "session"
	Tags      []string
	Source    string
	Limit     int
}

func (s *Store) ListMemories(f ListFilter) ([]*MemoryEntry, error) {
	query := `SELECT DISTINCT e.id FROM memory_entries e`
	var args []any
	var where []string

	if len(f.Tags) > 0 {
		query += ` JOIN memory_tags t ON t.memory_id = e.id`
		placeholders := make([]string, len(f.Tags))
		for i, tag := range f.Tags {
			placeholders[i] = "?"
			args = append(args, tag)
		}
		where = append(where, "t.tag IN ("+joinPlaceholders(placeholders)+")")
	}
	if f.Scope != "" {
		where = append(where, "e.scope = ?")
		args = append(args, f.Scope)
	}
	if f.SessionID != "" {
		where = append(where, "e.session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.Source != "" {
		where = append(where, "e.source = ?")
		args = append(args, f.Source)
	}

	if len(where) > 0 {
		query += " WHERE " + joinAnd(where)
	}
	query += " ORDER BY e.created_at DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	entries := make([]*MemoryEntry, 0, len(ids))
	for _, id := range ids {
		entry, err := s.GetMemory(id)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func joinPlaceholders(items []string) string {
	out := items[0]
	for _, s := range items[1:] {
		out += ", " + s
	}
	return out
}

func joinAnd(clauses []string) string {
	out := clauses[0]
	for _, c := range clauses[1:] {
		out += " AND " + c
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
