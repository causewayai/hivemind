# Hivemind Local Daemon Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build the Local Edition daemon — a persistent background service that AI harnesses connect to over MCP (Streamable HTTP) to write and retrieve memory entries, backed by SQLite + sqlite-vec. No CLI or human-facing client is built in this plan.

**Architecture:** A single Go binary (`cmd/hivemindd`) runs one `mcp.Server` exposing three tools (`memory_write`, `memory_query`, `list_scopes`) over `mcp.NewStreamableHTTPHandler`, bound to `127.0.0.1` only. Storage is a `internal/store` package wrapping a single `*sql.DB` (SQLite via `mattn/go-sqlite3`, WAL mode) with two structured tables (`memory_entries`, `memory_tags`) and one `sqlite-vec` `vec0` virtual table (`memory_vectors`), linked by SQLite's implicit rowid. Session scope is tracked purely at the application level — harnesses pass an explicit `session_id` on every call — so the daemon has no dependency on MCP transport-level session semantics.

**Tech Stack:** Go, `github.com/modelcontextprotocol/go-sdk` v1.7.0 (MCP, Streamable HTTP transport), `github.com/mattn/go-sqlite3` v1.14.22 (cgo SQLite driver), `github.com/asg017/sqlite-vec-go-bindings/cgo` (vector search, statically linked), `github.com/google/uuid`.

---

## Assumptions to confirm before starting

- Go module path is `github.com/causewayai/hivemind`.
- This directory (`/Users/johnament/causeway/hivemind`) is not yet a git repo. Task 1 runs `git init`.
- Default daemon port `8420`, default data dir `~/.hivemind/hivemind.db` — both overridable via env vars, see Task 2.
- Embedding dimension defaults to `768`. This must match whatever real embedding provider is plugged in later (Task 5 only ships a passthrough option and a non-semantic test stub).

## Explicitly out of scope for this plan

- CLI, web UI, or any human-facing client
- Scope promotion (session → user) — per the PRD this is a human action, not an MCP operation; no promotion path exists yet, so all data today only ever lands in `session` scope unless a test seeds `user` scope directly
- ETL bulk-write / `external_id` upsert path
- Auth, multi-user, team/sub-team scopes (PRD marks these N/A for Local Edition)
- Content policy hooks, audit logging (Cloud Edition only)
- A real embedding provider (OpenAI-compatible HTTP call, etc.) — Task 5 leaves a clean seam for this

---

### Task 1: Repository & Go module scaffolding

**Files:**
- Create: `.gitignore`
- Create: `go.mod`, `go.sum`
- Create: `cmd/hivemindd/main.go` (placeholder)
- Create: `internal/config/.gitkeep`, `internal/store/.gitkeep`, `internal/embedding/.gitkeep`, `internal/mcpserver/.gitkeep`

**Step 1: Initialize git and the Go module**

```bash
cd /Users/johnament/causeway/hivemind
git init
go mod init github.com/causewayai/hivemind
```

**Step 2: Create `.gitignore`**

```
/hivemindd
*.db
*.db-wal
*.db-shm
```

**Step 3: Create directory layout**

```bash
mkdir -p cmd/hivemindd internal/config internal/store internal/embedding internal/mcpserver
```

**Step 4: Write a placeholder `cmd/hivemindd/main.go`**

```go
package main

import "fmt"

func main() {
	fmt.Println("hivemindd: not yet implemented")
}
```

**Step 5: Verify it builds**

Run: `go build ./...`
Expected: no output, exit code 0.

**Step 6: Commit**

```bash
git add .gitignore go.mod cmd internal
git commit -m "chore: scaffold hivemindd Go module and directory layout"
```

---

### Task 2: Config loading

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Step 1: Write the failing test**

```go
// internal/config/config_test.go
package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "")
	t.Setenv("HIVEMIND_PORT", "")
	t.Setenv("HIVEMIND_EMBEDDING_DIM", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8420 {
		t.Errorf("Port = %d, want 8420", cfg.Port)
	}
	if cfg.EmbeddingDim != 768 {
		t.Errorf("EmbeddingDim = %d, want 768", cfg.EmbeddingDim)
	}
	if cfg.DataDir == "" {
		t.Errorf("DataDir is empty, want a default path under the user's home directory")
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "/tmp/hivemind-test")
	t.Setenv("HIVEMIND_PORT", "9000")
	t.Setenv("HIVEMIND_EMBEDDING_DIM", "1536")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/tmp/hivemind-test" {
		t.Errorf("DataDir = %q, want /tmp/hivemind-test", cfg.DataDir)
	}
	if cfg.Port != 9000 {
		t.Errorf("Port = %d, want 9000", cfg.Port)
	}
	if cfg.EmbeddingDim != 1536 {
		t.Errorf("EmbeddingDim = %d, want 1536", cfg.EmbeddingDim)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config/...`
Expected: FAIL — `undefined: Load` (package has no `config.go` yet).

**Step 3: Write minimal implementation**

```go
// internal/config/config.go
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	DataDir      string
	Port         int
	EmbeddingDim int
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:         8420,
		EmbeddingDim: 768,
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cfg.DataDir = filepath.Join(home, ".hivemind", "hivemind.db")

	if v := os.Getenv("HIVEMIND_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("HIVEMIND_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return nil, err
		}
		cfg.Port = port
	}
	if v := os.Getenv("HIVEMIND_EMBEDDING_DIM"); v != "" {
		dim, err := strconv.Atoi(v)
		if err != nil {
			return nil, err
		}
		cfg.EmbeddingDim = dim
	}

	return cfg, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config/...`
Expected: PASS (2 tests)

**Step 5: Commit**

```bash
rm internal/config/.gitkeep
git add internal/config
git commit -m "feat: add daemon config loading with env var overrides"
```

---

### Task 3: SQLite schema — structured store

**Files:**
- Create: `internal/store/schema.go`
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Step 1: Add dependencies**

```bash
go get github.com/mattn/go-sqlite3@v1.14.22
go get github.com/google/uuid@latest
```

**Step 2: Write the failing test**

```go
// internal/store/store_test.go
package store

import "testing"

func TestOpen_CreatesSchema(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

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
```

**Step 3: Write minimal implementation**

```go
// internal/store/schema.go
package store

const schema = `
CREATE TABLE IF NOT EXISTS memory_entries (
    id           TEXT PRIMARY KEY,
    content      TEXT NOT NULL,
    scope        TEXT NOT NULL CHECK (scope IN ('session','user')),
    session_id   TEXT,
    source       TEXT NOT NULL,
    source_type  TEXT NOT NULL CHECK (source_type IN ('harness','etl')),
    external_id  TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_entries_scope_session
    ON memory_entries(scope, session_id);

CREATE TABLE IF NOT EXISTS memory_tags (
    memory_id TEXT NOT NULL REFERENCES memory_entries(id) ON DELETE CASCADE,
    tag       TEXT NOT NULL,
    PRIMARY KEY (memory_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_memory_tags_tag ON memory_tags(tag);
`
```

```go
// internal/store/store.go
package store

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/...`
Expected: PASS

**Step 5: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat: add SQLite-backed structured store with schema bootstrap"
```

---

### Task 4: sqlite-vec vector table integration

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/schema.go`
- Test: `internal/store/vector_test.go`

**Step 1: Add dependency**

```bash
go get github.com/asg017/sqlite-vec-go-bindings/cgo@v0.0.1-alpha.36
```

**Step 2: Write the failing test**

```go
// internal/store/vector_test.go
package store

import "testing"

func TestVectorTable_InsertAndSearch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	near := make([]float32, 4)
	far := make([]float32, 4)
	for i := range near {
		near[i] = 0.1
		far[i] = 9.9
	}

	if err := s.insertVector(1, near); err != nil {
		t.Fatalf("insertVector(near) error = %v", err)
	}
	if err := s.insertVector(2, far); err != nil {
		t.Fatalf("insertVector(far) error = %v", err)
	}

	rowids, err := s.searchVectors(near, 1)
	if err != nil {
		t.Fatalf("searchVectors() error = %v", err)
	}
	if len(rowids) != 1 || rowids[0] != 1 {
		t.Errorf("searchVectors() = %v, want [1]", rowids)
	}
}
```

Note: this test uses a 4-dimensional vector table directly rather than the configured 768-dim production table, to keep the test fast and readable. Wire `insertVector`/`searchVectors` to accept the dimension from the schema so both cases share the same code path.

**Step 3: Write minimal implementation**

```go
// internal/store/schema.go — append to the schema constant, made a template so dimension is injectable
```

Replace the `schema` constant with a function so the vector table dimension is configurable:

```go
// internal/store/schema.go
package store

import "fmt"

const structuredSchema = `
CREATE TABLE IF NOT EXISTS memory_entries (
    id           TEXT PRIMARY KEY,
    content      TEXT NOT NULL,
    scope        TEXT NOT NULL CHECK (scope IN ('session','user')),
    session_id   TEXT,
    source       TEXT NOT NULL,
    source_type  TEXT NOT NULL CHECK (source_type IN ('harness','etl')),
    external_id  TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_entries_scope_session
    ON memory_entries(scope, session_id);

CREATE TABLE IF NOT EXISTS memory_tags (
    memory_id TEXT NOT NULL REFERENCES memory_entries(id) ON DELETE CASCADE,
    tag       TEXT NOT NULL,
    PRIMARY KEY (memory_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_memory_tags_tag ON memory_tags(tag);
`

func vectorSchema(dim int) string {
	return fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_vectors USING vec0(embedding float[%d]);`,
		dim,
	)
}
```

```go
// internal/store/store.go
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

type Store struct {
	db  *sql.DB
	dim int
}

func Open(path string, dim int) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(structuredSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(vectorSchema(dim)); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db, dim: dim}, nil
}

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
	defer rows.Close()

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
```

Note: `Open`'s signature changed to take `dim`. Update the Task 3 test call sites (`Open(dir+"/test.db")` → `Open(dir+"/test.db", 768)`) when you run this task.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/...`
Expected: PASS (all tests, including the updated Task 3 test)

**Step 5: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat: add sqlite-vec virtual table for embedding storage and KNN search"
```

---

### Task 5: Embedding provider interface

**Files:**
- Create: `internal/embedding/provider.go`
- Create: `internal/embedding/hash.go`
- Test: `internal/embedding/hash_test.go`

**Step 1: Write the failing test**

```go
// internal/embedding/hash_test.go
package embedding

import "testing"

func TestHashProvider_Deterministic(t *testing.T) {
	p := NewHashProvider(768)

	v1, err := p.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	v2, err := p.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if len(v1) != 768 {
		t.Errorf("len(v1) = %d, want 768", len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("HashProvider not deterministic at index %d: %f != %f", i, v1[i], v2[i])
		}
	}
}
```

**Step 2: Write minimal implementation**

```go
// internal/embedding/provider.go
package embedding

// Provider generates an embedding vector for freeform text.
// A caller (harness) may also supply a precomputed vector directly when
// writing a memory, bypassing Provider entirely — see PRD "Retrieval":
// the harness's own LLM is a valid embedding source.
type Provider interface {
	Embed(text string) ([]float32, error)
}
```

```go
// internal/embedding/hash.go
package embedding

import "hash/fnv"

// HashProvider is a deterministic, non-semantic stand-in for a real
// embedding model. It exists so the daemon is runnable and testable
// end-to-end without any external model configured. It must be swapped
// for a real provider before semantic search results are meaningful.
type HashProvider struct {
	dim int
}

func NewHashProvider(dim int) *HashProvider {
	return &HashProvider{dim: dim}
}

func (p *HashProvider) Embed(text string) ([]float32, error) {
	vec := make([]float32, p.dim)
	h := fnv.New32a()
	for i := 0; i < p.dim; i++ {
		h.Write([]byte{byte(i)})
		h.Write([]byte(text))
		sum := h.Sum32()
		vec[i] = float32(sum%2000)/1000.0 - 1.0 // spread into [-1, 1)
	}
	return vec, nil
}
```

**Step 3: Run test to verify it passes**

Run: `go test ./internal/embedding/...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/embedding
git commit -m "feat: add pluggable embedding provider interface with hash-based stub"
```

---

### Task 6: Store write path (CreateMemory)

**Files:**
- Create: `internal/store/memory.go`
- Test: `internal/store/memory_test.go`

**Step 1: Write the failing test**

```go
// internal/store/memory_test.go
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
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/...`
Expected: FAIL — `undefined: CreateMemoryInput` / `undefined: GetMemory`

**Step 3: Write minimal implementation**

```go
// internal/store/memory.go
package store

import (
	"database/sql"
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
```

Add the transaction-aware vector insert helper next to `insertVector` in `internal/store/store.go`:

```go
func (s *Store) insertVectorTx(tx *sql.Tx, rowid int64, embedding []float32) error {
	blob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO memory_vectors(rowid, embedding) VALUES (?, ?)`, rowid, blob)
	return err
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/...`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: add CreateMemory/GetMemory with tag and vector persistence"
```

---

### Task 7: Store read path (ListMemories with filters)

**Files:**
- Modify: `internal/store/memory.go`
- Test: `internal/store/memory_test.go`

**Step 1: Write the failing test**

```go
// append to internal/store/memory_test.go
func TestListMemories_FilterByScopeAndTag(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

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
```

**Step 2: Write minimal implementation**

```go
// append to internal/store/memory.go
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
```

**Step 3: Run test to verify it passes**

Run: `go test ./internal/store/...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/store
git commit -m "feat: add ListMemories with scope/session/tag/source filtering"
```

---

### Task 8: Hybrid retrieval (semantic + filter)

**Files:**
- Modify: `internal/store/memory.go`
- Test: `internal/store/memory_test.go`

**Step 1: Write the failing test**

```go
// append to internal/store/memory_test.go
func TestQuery_HybridSemanticAndTagFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.db", 4)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

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
```

**Step 2: Write minimal implementation**

Vector search runs first over a wider candidate pool, then structured filters narrow it — the `vec0` build in use here has no verified support for pre-filtering by auxiliary columns, so post-filtering in application code is the safe default; revisit if `sqlite-vec` adds documented partition-key support later.

```go
// append to internal/store/memory.go
type QueryInput struct {
	Embedding []float32 // required; caller (MCP layer) resolves text -> vector before calling
	Tags      []string
	Scope     string
	SessionID string
	Source    string
	TopK      int
}

func (s *Store) Query(q QueryInput) ([]*MemoryEntry, error) {
	if q.TopK <= 0 {
		q.TopK = 10
	}

	candidatePool := q.TopK * 5
	rowIDs, err := s.searchVectors(q.Embedding, candidatePool)
	if err != nil {
		return nil, err
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
	query := `SELECT id FROM memory_entries WHERE rowid IN (` + joinPlaceholders(placeholders) + `)`
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

	byID := make(map[string]bool, len(rowIDs))
	for _, id := range ids {
		byID[id] = true
	}

	results := make([]*MemoryEntry, 0, len(ids))
	for _, id := range ids {
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
```

Note: vector search preserves distance order from `searchVectors`, but the final loop above iterates `ids` in the order returned by the `SELECT ... WHERE rowid IN (...)` query, which SQLite does **not** guarantee matches `rowIDs` order. Fix before considering this task done: change the final ranking to iterate `rowIDs` (in vector-distance order) and look up each in a `map[int64]string` built from the `ids` query, skipping rowids not present in the filtered set. Write this correction into the implementation, then re-run the test — the test as written only has one candidate that matches, so it will pass even with the ordering bug, but ordering must be correct for multi-result queries in Task 10.

**Step 3: Run test to verify it passes**

Run: `go test ./internal/store/...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/store
git commit -m "feat: add hybrid semantic + tag/scope filtered Query"
```

---

### Task 9: MCP tool — memory_write

**Files:**
- Create: `internal/mcpserver/server.go`
- Create: `internal/mcpserver/write.go`
- Test: `internal/mcpserver/write_test.go`

**Step 1: Add MCP SDK dependency**

```bash
go get github.com/modelcontextprotocol/go-sdk@v1.7.0
```

**Step 2: Write the failing test**

Test the handler function directly (no HTTP transport yet — that's Task 12), since the SDK's `AddTool` wires a plain Go function.

```go
// internal/mcpserver/write_test.go
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
```

**Step 3: Write minimal implementation**

```go
// internal/mcpserver/server.go
package mcpserver

import (
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
)

type Server struct {
	store     *store.Store
	embedder  embedding.Provider
}

func New(s *store.Store, embedder embedding.Provider) *Server {
	return &Server{store: s, embedder: embedder}
}
```

```go
// internal/mcpserver/write.go
package mcpserver

import "context"

// MemoryWriteInput is the memory_write tool's input schema.
// PRD "Interfaces -> MCP Interface": harnesses have full CRUD on their own
// session scope only — writing is therefore always scoped to SessionID,
// there is no Scope field to override that.
type MemoryWriteInput struct {
	SessionID string   `json:"session_id" jsonschema:"the harness-generated session identifier"`
	Content   string   `json:"content" jsonschema:"the memory content to store"`
	Source    string   `json:"source" jsonschema:"identifier of the harness writing this memory"`
	Tags      []string `json:"tags,omitempty" jsonschema:"optional labels for filtering"`
	Embedding []float32 `json:"embedding,omitempty" jsonschema:"optional precomputed embedding; if omitted the daemon generates one"`
}

type MemoryWriteOutput struct {
	ID string `json:"id"`
}

func (s *Server) handleMemoryWrite(ctx context.Context, in MemoryWriteInput) (*MemoryWriteOutput, error) {
	embVec := in.Embedding
	if embVec == nil {
		var err error
		embVec, err = s.embedder.Embed(in.Content)
		if err != nil {
			return nil, err
		}
	}

	entry, err := s.store.CreateMemory(store.CreateMemoryInput{
		Content:    in.Content,
		Scope:      "session",
		SessionID:  in.SessionID,
		Source:     in.Source,
		SourceType: "harness",
		Tags:       in.Tags,
		Embedding:  embVec,
	})
	if err != nil {
		return nil, err
	}
	return &MemoryWriteOutput{ID: entry.ID}, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/mcpserver/...`
Expected: PASS

**Step 5: Commit**

```bash
git add go.mod go.sum internal/mcpserver
git commit -m "feat: add memory_write handler, hard-scoped to session"
```

---

### Task 10: MCP tool — memory_query (with session isolation)

**Files:**
- Create: `internal/mcpserver/query.go`
- Test: `internal/mcpserver/query_test.go`

**Step 1: Write the failing test**

```go
// internal/mcpserver/query_test.go
package mcpserver

import (
	"context"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
)

func TestMemoryQuery_SessionIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir+"/test.db", 8)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer s.Close()

	srv := New(s, embedding.NewHashProvider(8))
	ctx := context.Background()

	if _, err := srv.handleMemoryWrite(ctx, MemoryWriteInput{SessionID: "sess-A", Content: "A's secret", Source: "harness-a"}); err != nil {
		t.Fatalf("write A error = %v", err)
	}
	if _, err := srv.handleMemoryWrite(ctx, MemoryWriteInput{SessionID: "sess-B", Content: "B's secret", Source: "harness-b"}); err != nil {
		t.Fatalf("write B error = %v", err)
	}
	if _, err := s.CreateMemory(store.CreateMemoryInput{
		Content: "shared team fact", Scope: "user", Source: "seed", SourceType: "harness",
		Embedding: make([]float32, 8),
	}); err != nil {
		t.Fatalf("seed user-scope entry error = %v", err)
	}

	out, err := srv.handleMemoryQuery(ctx, MemoryQueryInput{SessionID: "sess-A", Query: "secret", TopK: 10})
	if err != nil {
		t.Fatalf("handleMemoryQuery() error = %v", err)
	}

	var sawOwn, sawOthers, sawShared bool
	for _, e := range out.Results {
		switch e.Content {
		case "A's secret":
			sawOwn = true
		case "B's secret":
			sawOthers = true
		case "shared team fact":
			sawShared = true
		}
	}
	if !sawOwn {
		t.Errorf("expected to see own session-scope entry")
	}
	if sawOthers {
		t.Errorf("must not see another session's session-scope entry")
	}
	if !sawShared {
		t.Errorf("expected to see user-scope entry")
	}
}
```

**Step 2: Write minimal implementation**

Session isolation is enforced by issuing two `store.Query` calls (own session-scope + all user-scope) and merging, rather than trying to express "session_id = X OR scope = user" as a single filter — `store.Query`'s `ListFilter`/`QueryInput` only support one scope at a time by design (Task 8).

```go
// internal/mcpserver/query.go
package mcpserver

import (
	"context"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryQueryInput is the memory_query tool's input schema.
// PRD "Interfaces -> MCP Interface": harnesses get read-only access to
// scopes beyond their own session, so this never exposes a write path.
type MemoryQueryInput struct {
	SessionID string   `json:"session_id" jsonschema:"the calling harness's session identifier"`
	Query     string   `json:"query,omitempty" jsonschema:"free text for semantic search"`
	Tags      []string `json:"tags,omitempty"`
	Source    string   `json:"source,omitempty"`
	TopK      int      `json:"top_k,omitempty"`
}

type MemoryQueryOutput struct {
	Results []*store.MemoryEntry `json:"results"`
}

func (s *Server) handleMemoryQuery(ctx context.Context, in MemoryQueryInput) (*MemoryQueryOutput, error) {
	if in.TopK <= 0 {
		in.TopK = 10
	}

	embVec, err := s.embedder.Embed(in.Query)
	if err != nil {
		return nil, err
	}

	own, err := s.store.Query(store.QueryInput{
		Embedding: embVec, Tags: in.Tags, Source: in.Source, TopK: in.TopK,
		Scope: "session", SessionID: in.SessionID,
	})
	if err != nil {
		return nil, err
	}

	shared, err := s.store.Query(store.QueryInput{
		Embedding: embVec, Tags: in.Tags, Source: in.Source, TopK: in.TopK,
		Scope: "user",
	})
	if err != nil {
		return nil, err
	}

	results := append(own, shared...)
	if len(results) > in.TopK {
		results = results[:in.TopK]
	}
	return &MemoryQueryOutput{Results: results}, nil
}
```

**Step 3: Run test to verify it passes**

Run: `go test ./internal/mcpserver/...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/mcpserver
git commit -m "feat: add memory_query handler with session-isolated + shared-scope retrieval"
```

---

### Task 11: MCP tool — list_scopes

**Files:**
- Create: `internal/mcpserver/scopes.go`
- Test: `internal/mcpserver/scopes_test.go`

**Step 1: Write the failing test**

```go
// internal/mcpserver/scopes_test.go
package mcpserver

import (
	"context"
	"reflect"
	"testing"
)

func TestListScopes(t *testing.T) {
	srv := New(nil, nil)
	out, err := srv.handleListScopes(context.Background(), ListScopesInput{})
	if err != nil {
		t.Fatalf("handleListScopes() error = %v", err)
	}
	want := []string{"session", "user"}
	if !reflect.DeepEqual(out.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", out.Scopes, want)
	}
}
```

**Step 2: Write minimal implementation**

```go
// internal/mcpserver/scopes.go
package mcpserver

import "context"

type ListScopesInput struct{}

type ListScopesOutput struct {
	Scopes []string `json:"scopes"`
}

// Team and sub-team scopes are N/A for the Local Edition per the PRD's
// Memory Scopes table.
func (s *Server) handleListScopes(ctx context.Context, in ListScopesInput) (*ListScopesOutput, error) {
	return &ListScopesOutput{Scopes: []string{"session", "user"}}, nil
}
```

**Step 3: Run test to verify it passes**

Run: `go test ./internal/mcpserver/...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/mcpserver
git commit -m "feat: add list_scopes handler"
```

---

### Task 12: Daemon entrypoint — wire MCP over Streamable HTTP

**Files:**
- Modify: `cmd/hivemindd/main.go`
- Test: `cmd/hivemindd/main_test.go`

**Step 1: Write the failing integration test**

This test starts the real HTTP handler on an in-process `httptest.Server` and drives it with the MCP SDK's own client, exercising the full write → query round trip.

```go
// cmd/hivemindd/main_test.go
package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/mcpserver"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDaemon_WriteThenQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir+"/test.db", 768)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer s.Close()

	mcpSrv := buildMCPServer(s, embedding.NewHashProvider(768))
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	ctx := context.Background()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer session.Close()

	writeRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_write",
		Arguments: map[string]any{
			"session_id": "sess-1",
			"content":    "the build is broken on main",
			"source":     "test-harness",
			"tags":       []string{"ci"},
		},
	})
	if err != nil || writeRes.IsError {
		t.Fatalf("memory_write call failed: err=%v res=%+v", err, writeRes)
	}

	queryRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_query",
		Arguments: map[string]any{
			"session_id": "sess-1",
			"query":      "build",
			"top_k":      5,
		},
	})
	if err != nil || queryRes.IsError {
		t.Fatalf("memory_query call failed: err=%v res=%+v", err, queryRes)
	}
}
```

Note: the exact `mcp.Client` / `mcp.StreamableClientTransport` constructor shapes should be checked against `github.com/modelcontextprotocol/go-sdk@v1.7.0`'s own `examples/` directory when writing this test — the SDK's client API surface is the part of this plan researched with least certainty; adjust field/method names to match what actually compiles, the intent (connect, call `memory_write`, call `memory_query`, assert no errors) is what matters.

**Step 2: Write minimal implementation**

```go
// cmd/hivemindd/main.go
package main

import (
	"log"
	"net/http"

	"github.com/causewayai/hivemind/internal/config"
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/mcpserver"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func buildMCPServer(s *store.Store, embedder embedding.Provider) *mcp.Server {
	srv := mcpserver.New(s, embedder)

	server := mcp.NewServer(&mcp.Implementation{Name: "hivemind", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_write", Description: "Write a memory entry, scoped to the calling session"}, srv.HandleMemoryWrite)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_query", Description: "Query memories via semantic search and/or tag filters"}, srv.HandleMemoryQuery)
	mcp.AddTool(server, &mcp.Tool{Name: "list_scopes", Description: "List memory scopes available in this edition"}, srv.HandleListScopes)
	return server
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	s, err := store.Open(cfg.DataDir, cfg.EmbeddingDim)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer s.Close()

	embedder := embedding.NewHashProvider(cfg.EmbeddingDim)
	mcpSrv := buildMCPServer(s, embedder)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpSrv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	addr := "127.0.0.1:" + fmtPort(cfg.Port)
	log.Printf("hivemindd listening on %s (loopback only)", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
```

Rename the three unexported `handleMemoryWrite` / `handleMemoryQuery` / `handleListScopes` methods from Tasks 9–11 to exported `HandleMemoryWrite` / `HandleMemoryQuery` / `HandleListScopes` (`mcp.AddTool` needs an exported-shaped function value; method exportedness itself doesn't matter to Go, but keeping the naming consistent with what `main.go` references above avoids confusion) — add a small `fmtPort(int) string` helper (`strconv.Itoa`) in `main.go`.

**Step 3: Run test to verify it passes**

Run: `go test ./...`
Expected: PASS across all packages.

**Step 4: Commit**

```bash
git add cmd internal
git commit -m "feat: wire MCP tools onto Streamable HTTP daemon entrypoint"
```

---

### Task 13: Build and manual smoke test

**Files:**
- Create: `Makefile`

**Step 1: Write the Makefile**

```makefile
.PHONY: build test run

build:
	CGO_ENABLED=1 go build -o hivemindd ./cmd/hivemindd

test:
	go test ./...

run: build
	./hivemindd
```

**Step 2: Build**

Run: `make build`
Expected: produces `./hivemindd`, exit code 0.

Note: `CGO_ENABLED=1` is required because both `mattn/go-sqlite3` and the `sqlite-vec-go-bindings/cgo` package compile C code into the binary. The result is a single file with no separate `.so`/`.dylib` to distribute, but it is **not** a fully static binary — it still dynamically links the platform's libc. This is a lighter guarantee than the PRD's "single self-contained binary" line might imply for cross-platform distribution (e.g., a Linux build won't run against an incompatible glibc on another box). Flag this as a [[local-daemon-static-binary]] open item in `docs/DESIGN.md` — the fix, if a truly static/portable binary is required, is switching to the WASM-based `sqlite-vec` bindings paired with `ncruces/go-sqlite3` (cgo-free), which is a larger change deferred to a follow-up plan.

**Step 3: Manual smoke test**

```bash
HIVEMIND_PORT=8420 HIVEMIND_DATA_DIR=/tmp/hivemind-smoke.db ./hivemindd &
sleep 1
curl -s -X POST http://127.0.0.1:8420/ \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0.0.1"}}}'
kill %1
```

Expected: a JSON-RPC response containing `"result"` with server capabilities (exact shape depends on the SDK version — the point of this step is confirming the daemon accepts a connection on loopback and speaks JSON-RPC back, not matching an exact payload).

**Step 4: Commit**

```bash
git add Makefile
git commit -m "chore: add build/test/run Makefile targets"
```

---

## After this plan

Natural follow-ups, each a separate plan:
- Real embedding provider (HTTP call to an OpenAI-compatible endpoint, or accepting the connecting harness's own embedding output as the default rather than the hash stub)
- CLI: human-facing scope promotion (session → user), query, tag, delete
- ETL bulk-write path with `(source, external_id, scope)` upsert
- Resolve the static-binary distribution question noted in Task 13
