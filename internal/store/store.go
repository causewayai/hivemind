// Package store implements the SQLite-backed structured and vector storage
// for the Local Edition daemon: memory entries, tags, and their embeddings.
package store

import (
	"database/sql"
	"os"
	"path/filepath"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

func init() {
	sqlite_vec.Auto()
}

// Store wraps a single SQLite database holding both the structured memory
// tables and the sqlite-vec vector table.
type Store struct {
	db  *sql.DB
	dim int
}

// Open opens (creating if necessary) a SQLite database at path, bootstraps
// its schema, and configures a dim-dimensional vector table for embeddings.
func Open(path string, dim int) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(structuredSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(vectorSchema(dim)); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db, dim: dim}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) insertVector(rowid int64, embedding []float32) error {
	blob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO memory_vectors(rowid, embedding) VALUES (?, ?)`, rowid, blob)
	return err
}

func (s *Store) insertVectorTx(tx *sql.Tx, rowid int64, embedding []float32) error {
	blob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO memory_vectors(rowid, embedding) VALUES (?, ?)`, rowid, blob)
	return err
}

func (s *Store) searchVectors(query []float32, topK int) ([]int64, error) {
	blob, err := sqlite_vec.SerializeFloat32(query)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT rowid FROM memory_vectors WHERE embedding MATCH ? ORDER BY distance LIMIT ?`,
		blob, topK,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type vecMatch struct {
	RowID    int64
	Distance float64
}

func (s *Store) searchVectorsWithDistance(query []float32, topK int) ([]vecMatch, error) {
	blob, err := sqlite_vec.SerializeFloat32(query)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT rowid, distance FROM memory_vectors WHERE embedding MATCH ? ORDER BY distance LIMIT ?`,
		blob, topK,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var matches []vecMatch
	for rows.Next() {
		var m vecMatch
		if err := rows.Scan(&m.RowID, &m.Distance); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}
