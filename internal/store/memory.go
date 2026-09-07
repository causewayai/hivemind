package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MemoryEntry is a single stored memory, along with its tags.
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

// CreateMemoryInput are the fields needed to persist a new MemoryEntry.
type CreateMemoryInput struct {
	Content    string
	Scope      string // "session" or "user"
	SessionID  string // required when Scope == "session"
	Source     string
	SourceType string // "harness" or "etl"
	Tags       []string
	Embedding  []float32
	// ExternalID, when non-empty, makes the write an idempotent upsert keyed on
	// (Source, ExternalID, Scope): if a row already exists it is returned
	// unchanged and no tags/embedding are written. Empty means a plain insert.
	ExternalID string
}

// CreateMemory inserts a new memory entry, its tags, and (if provided) its
// embedding, all within one transaction.
func (s *Store) CreateMemory(in CreateMemoryInput) (*MemoryEntry, error) {
	now := time.Now().UTC()
	entry := &MemoryEntry{
		ID:         uuid.NewString(),
		Content:    in.Content,
		Scope:      in.Scope,
		SessionID:  in.SessionID,
		Source:     in.Source,
		SourceType: in.SourceType,
		ExternalID: in.ExternalID,
		Tags:       in.Tags,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	// A no-op after a successful Commit (returns sql.ErrTxDone, safe to ignore).
	defer func() { _ = tx.Rollback() }()

	var rowID int64
	if in.ExternalID == "" {
		res, err := tx.Exec(
			`INSERT INTO memory_entries (id, content, scope, session_id, source, source_type, external_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.ID, entry.Content, entry.Scope, nullable(entry.SessionID), entry.Source, entry.SourceType,
			nil, entry.CreatedAt.Format(time.RFC3339), entry.UpdatedAt.Format(time.RFC3339),
		)
		if err != nil {
			return nil, err
		}
		if rowID, err = res.LastInsertId(); err != nil {
			return nil, err
		}
	} else {
		res, err := tx.Exec(
			`INSERT INTO memory_entries (id, content, scope, session_id, source, source_type, external_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(source, external_id, scope) WHERE external_id IS NOT NULL DO NOTHING`,
			entry.ID, entry.Content, entry.Scope, nullable(entry.SessionID), entry.Source, entry.SourceType,
			entry.ExternalID, entry.CreatedAt.Format(time.RFC3339), entry.UpdatedAt.Format(time.RFC3339),
		)
		if err != nil {
			return nil, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			// Row already existed — commit the (empty) tx and return the existing entry.
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			existing, err := s.GetMemoryByExternalID(entry.Source, entry.ExternalID, entry.Scope)
			if err != nil {
				return nil, err
			}
			if existing == nil {
				return nil, fmt.Errorf("upsert on (%s, %s, %s) hit a conflict but the row is gone",
					entry.Source, entry.ExternalID, entry.Scope)
			}
			return existing, nil
		}
		if rowID, err = res.LastInsertId(); err != nil {
			return nil, err
		}
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

// GetMemory fetches a single memory entry, including its tags, by ID.
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
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		entry.Tags = append(entry.Tags, tag)
	}
	return entry, rows.Err()
}

// GetMemoryByExternalID fetches the single entry matching the composite ETL
// key, or (nil, nil) if there is no match.
func (s *Store) GetMemoryByExternalID(source, externalID, scope string) (*MemoryEntry, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM memory_entries WHERE source = ? AND external_id = ? AND scope = ?`,
		source, externalID, scope,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetMemory(id)
}

// ListFilter narrows ListMemories to entries matching all given criteria;
// zero-valued fields are unconstrained.
type ListFilter struct {
	Scope      string // "session" or "user"; empty means both
	SessionID  string // required when filtering scope == "session"
	Tags       []string
	Source     string
	ExternalID string // exact match on the ETL external key
	Limit      int
}

// ListMemories returns entries matching f, most recently created first.
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
	if f.ExternalID != "" {
		where = append(where, "e.external_id = ?")
		args = append(args, f.ExternalID)
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
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
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

// defaultMaxDistance is a relevance cutoff (L2 distance) applied to vector
// search candidates before structured filters run. Without it, "hybrid"
// retrieval degrades to "tag filter with re-ranking" — a candidate that
// matches on tags but is semantically unrelated to the query would still
// surface, since tag-matching alone doesn't bound how dissimilar a vector is.
// This value is a heuristic tuned against HashProvider's spread ([-1,1) per
// dimension); it will need recalibration once a real embedding provider
// (with its own characteristic distance scale) replaces the stub.
const defaultMaxDistance = 1.0

// QueryInput configures a hybrid semantic + structured-filter search.
type QueryInput struct {
	Embedding []float32 // required; caller (MCP layer) resolves text -> vector before calling
	Tags      []string
	Scope     string
	SessionID string
	Source    string
	TopK      int
}

// Query returns up to TopK entries ranked by embedding distance to
// q.Embedding, restricted to those within defaultMaxDistance and matching
// every structured filter set on q.
func (s *Store) Query(q QueryInput) ([]*MemoryEntry, error) {
	if q.TopK <= 0 {
		q.TopK = 10
	}

	candidatePool := q.TopK * 5
	matches, err := s.searchVectorsWithDistance(q.Embedding, candidatePool)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}

	rowIDs := make([]int64, 0, len(matches))
	for _, m := range matches {
		if m.Distance > defaultMaxDistance {
			continue
		}
		rowIDs = append(rowIDs, m.RowID)
	}
	if len(rowIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(rowIDs))
	args := make([]any, len(rowIDs))
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT rowid, id FROM memory_entries WHERE rowid IN (` + joinPlaceholders(placeholders) + `)`
	if q.Scope != "" {
		query += " AND scope = ?"
		args = append(args, q.Scope)
	}
	if q.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, q.SessionID)
	}
	if q.Source != "" {
		query += " AND source = ?"
		args = append(args, q.Source)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	idByRowID := make(map[int64]string)
	for rows.Next() {
		var rowID int64
		var id string
		if err := rows.Scan(&rowID, &id); err != nil {
			return nil, err
		}
		idByRowID[rowID] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]*MemoryEntry, 0, len(idByRowID))
	for _, rowID := range rowIDs {
		id, ok := idByRowID[rowID]
		if !ok {
			continue
		}
		entry, err := s.GetMemory(id)
		if err != nil {
			return nil, err
		}
		if len(q.Tags) > 0 && !hasAnyTag(entry.Tags, q.Tags) {
			continue
		}
		results = append(results, entry)
		if len(results) == q.TopK {
			break
		}
	}
	return results, nil
}

func hasAnyTag(entryTags, wantTags []string) bool {
	set := make(map[string]bool, len(entryTags))
	for _, t := range entryTags {
		set[t] = true
	}
	for _, want := range wantTags {
		if set[want] {
			return true
		}
	}
	return false
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
