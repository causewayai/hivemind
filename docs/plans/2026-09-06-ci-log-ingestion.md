# CI Log Ingestion Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Cache GitHub Actions run/job logs locally so a harness that already fetched a run's logs — this session or a past one — never re-hits the GitHub API for it.

**Architecture:** Three moving parts. (1) `hivemindd` (the existing daemon) gains an `external_id`-keyed idempotent upsert on `memory_write`, a structured-only (no-embedding) lookup path on `memory_query`, and an in-process hourly cleanup ticker. (2) A **new second binary `hivemind`** (client CLI, same repo/release) with a `ci-logs` subcommand that checks the cache, and on a miss shells out to the user's `gh`, then writes a run summary + per-failed-job entries and saves the raw log to `~/.hivemind/ci-logs/`. The CLI lazily autostarts `hivemindd`, discovering it via a `~/.hivemind/daemon.port` file the daemon writes on boot. (3) A Claude Code `PreToolUse` hook (`hivemind hook claude`) that transparently rewrites `gh run view --log …` commands to `hivemind ci-logs …` before execution, modeled on `../rtk`'s `src/hooks/hook_cmd.rs`.

**Tech Stack:** Go 1.25, cgo (`mattn/go-sqlite3` + `asg017/sqlite-vec`), `modelcontextprotocol/go-sdk` (MCP over Streamable HTTP on loopback TCP), stdlib `flag`/`os/exec`/`syscall`. No new module dependencies.

**Reference material (read before starting):**
- `docs/plans/2026-09-06-ci-log-ingestion-design.md` — the requirements + every resolved decision. Non-negotiable; this plan implements it.
- `docs/PRD.md` lines 34–43, 81–83 — the memory envelope and the ETL upsert requirement (composite key `(source, external_id, scope)`).
- `../rtk/src/hooks/hook_cmd.rs` — a production Claude Code `PreToolUse` transparent-rewrite hook. `run_claude()` / `process_claude_payload_from_decision()` is the exact JSON contract to copy.
- `docs/DESIGN.md` — running log of resolved design questions; append to it (Task 24).

**Conventions in this codebase (match them):**
- Tests live in the same package (`package store`, not `store_test`), use `t.TempDir()` + `store.Open(dir+"/test.db", dim)`, and are written as explicit functions (no table-driven style). `t.Setenv` for env.
- `golangci-lint` runs in CI via `make check`. `revive` rules `exported` + `package-comments` are on: every exported symbol and every package needs a doc comment.
- **depguard layering** (`.golangci.yml`): `internal/config`, `internal/embedding`, `internal/store` must NOT import any other `github.com/causewayai/hivemind/...` package. `internal/mcpserver` must NOT import `.../cmd`. The new `internal/cilog` package has no restriction and MAY import `internal/store` + `internal/config`. `cmd/hivemind` MAY import anything under `internal/`.
- Error strings: lower-case, no trailing punctuation (`revive` `error-strings`).
- Commit after every task with a `feat:` / `test:` / `chore:` prefixed message. Keep the working tree green (`make check`) at each commit.

---

## Phase 1 — Store: `external_id` persistence, idempotent upsert, structured lookup

### Task 1: Partial unique index on `(source, external_id, scope)`

**Files:**
- Modify: `internal/store/schema.go`
- Test: `internal/store/schema_test.go` (create)

**Step 1: Write the failing test**

```go
// internal/store/schema_test.go
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
	// Two NULL external_id rows with same source/scope -> allowed (partial index).
	if err := insert("d", "seed", "", "user"); err != nil {
		t.Fatalf("first NULL external_id insert error = %v", err)
	}
	if err := insert("e", "seed", "", "user"); err != nil {
		t.Fatalf("second NULL external_id insert should be allowed by partial index, got %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSchema_PartialUniqueIndexOnExternalID -v`
Expected: FAIL — the second `insert("b", …)` succeeds because no unique index exists yet.

**Step 3: Add the index**

In `internal/store/schema.go`, append to the `structuredSchema` const string (after the `idx_memory_tags_tag` index, before the closing backtick):

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entries_source_extid_scope
    ON memory_entries(source, external_id, scope)
    WHERE external_id IS NOT NULL;
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSchema_PartialUniqueIndexOnExternalID -v`
Expected: PASS

**Step 5: Full check + commit**

```bash
make check
git add internal/store/schema.go internal/store/schema_test.go
git commit -m "feat: partial unique index on (source, external_id, scope)"
```

---

### Task 2: `CreateMemory` persists `ExternalID` and upserts idempotently

**Context:** `CreateMemory` currently binds a literal `nil` for the `external_id` column (`internal/store/memory.go` ~line 61) and always plain-`INSERT`s. We add an `ExternalID` field and, when it is set, switch to insert-if-absent semantics keyed on the Task 1 index — CI logs for a finished run are immutable, so a conflict means "already cached; return what's there".

**Files:**
- Modify: `internal/store/memory.go` (`CreateMemoryInput`, `CreateMemory`, add `GetMemoryByExternalID`)
- Test: `internal/store/memory_test.go`

**Step 1: Write the failing tests**

```go
// append to internal/store/memory_test.go

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
```

Add this shared helper once (put it in `internal/store/memory_test.go` near the top, and reuse it — do NOT redefine it in other `_test.go` files in this package):

```go
func openTestStore(t *testing.T, dim int) *Store {
	t.Helper()
	s, err := Open(t.TempDir()+"/test.db", dim)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
```

**Step 2: Run to verify failure**

Run: `go test ./internal/store/ -run 'TestCreateMemory_ExternalID|TestCreateMemory_Upsert|TestCreateMemory_SameExternalID|TestGetMemoryByExternalID' -v`
Expected: FAIL to compile — `ExternalID` field and `GetMemoryByExternalID` don't exist.

**Step 3: Implement**

In `internal/store/memory.go`:

1. Add to `CreateMemoryInput`:
```go
	// ExternalID, when non-empty, makes the write an idempotent upsert keyed on
	// (Source, ExternalID, Scope): if a row already exists it is returned
	// unchanged and no tags/embedding are written. Empty means a plain insert.
	ExternalID string
```

2. In `CreateMemory`, set `ExternalID: in.ExternalID` on the `entry` literal, and replace the single `INSERT` + `LastInsertId` block with a branch:

```go
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
			return s.GetMemoryByExternalID(entry.Source, entry.ExternalID, entry.Scope)
		}
		if rowID, err = res.LastInsertId(); err != nil {
			return nil, err
		}
	}
```

Keep the existing tag-insert loop and `insertVectorTx` call and final `tx.Commit()` exactly as they are — they now run only on a real insert.

3. Add `GetMemoryByExternalID` (put it right after `GetMemory`):

```go
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
```

Add `"database/sql"` and `"errors"` to the imports if not present.

**Step 4: Run to verify pass**

Run: `go test ./internal/store/ -v`
Expected: PASS (all existing store tests + the 4 new ones).

**Step 5: check + commit**

```bash
make check
git add internal/store/memory.go internal/store/memory_test.go
git commit -m "feat: external_id-keyed idempotent upsert in CreateMemory"
```

---

### Task 3: Structured lookup by `external_id` (no embedding)

**Context:** `ListMemories` already filters by scope/session/tags/source, newest-first, no vectors. It just lacks an `external_id` filter. Add it.

**Files:**
- Modify: `internal/store/memory.go` (`ListFilter`, `ListMemories`)
- Test: `internal/store/memory_test.go`

**Step 1: Failing test**

```go
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
```

**Step 2: Run — expect compile failure** (`ExternalID` not on `ListFilter`).
Run: `go test ./internal/store/ -run TestListMemories_FilterByExternalID -v`

**Step 3: Implement**

In `ListFilter` add:
```go
	ExternalID string // exact match on the ETL external key
```

In `ListMemories`, after the `f.Source` clause:
```go
	if f.ExternalID != "" {
		where = append(where, "e.external_id = ?")
		args = append(args, f.ExternalID)
	}
```

**Step 4: Run — expect PASS.** `go test ./internal/store/ -v`

**Step 5: check + commit**

```bash
make check
git add internal/store/memory.go internal/store/memory_test.go
git commit -m "feat: ListMemories external_id filter for structured lookup"
```

---

## Phase 2 — MCP handlers

### Task 4: `memory_write` accepts optional `scope` / `source_type` / `external_id`

**Files:**
- Modify: `internal/mcpserver/write.go`
- Test: `internal/mcpserver/write_test.go`

**Step 1: Failing tests**

```go
// append to internal/mcpserver/write_test.go

func TestMemoryWrite_ExplicitUserScopeEtlExternalID(t *testing.T) {
	s := openMCPTestStore(t, 8)
	srv := New(s, embedding.NewHashProvider(8))

	out, err := srv.handleMemoryWrite(context.Background(), MemoryWriteInput{
		Content: "run summary", Source: "github-actions",
		Scope: "user", SourceType: "etl", ExternalID: "o/r#1",
	})
	if err != nil {
		t.Fatalf("handleMemoryWrite() error = %v", err)
	}
	got, _ := s.GetMemory(out.ID)
	if got.Scope != "user" || got.SourceType != "etl" || got.ExternalID != "o/r#1" {
		t.Fatalf("stored entry = %+v, want scope=user source_type=etl external_id=o/r#1", got)
	}
}

func TestMemoryWrite_UpsertReturnsSameID(t *testing.T) {
	s := openMCPTestStore(t, 8)
	srv := New(s, embedding.NewHashProvider(8))
	in := MemoryWriteInput{Content: "a", Source: "github-actions", Scope: "user", SourceType: "etl", ExternalID: "o/r#1"}

	first, err := srv.handleMemoryWrite(context.Background(), in)
	if err != nil {
		t.Fatalf("first write error = %v", err)
	}
	in.Content = "b"
	second, err := srv.handleMemoryWrite(context.Background(), in)
	if err != nil {
		t.Fatalf("second write error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent upsert must return same ID: %q vs %q", first.ID, second.ID)
	}
}

func TestMemoryWrite_RejectsInvalidScope(t *testing.T) {
	s := openMCPTestStore(t, 8)
	srv := New(s, embedding.NewHashProvider(8))
	_, err := srv.handleMemoryWrite(context.Background(), MemoryWriteInput{
		Content: "x", Source: "h", Scope: "team",
	})
	if err == nil {
		t.Fatal("scope=team must be rejected")
	}
}
```

Add the shared helper (once) to `internal/mcpserver/write_test.go`:

```go
func openMCPTestStore(t *testing.T, dim int) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir()+"/test.db", dim)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
```

(and update the existing `TestMemoryWrite_DefaultsToSessionScope` to use it if you want — not required.)

**Step 2: Run — expect compile failure.**
Run: `go test ./internal/mcpserver/ -run TestMemoryWrite -v`

**Step 3: Implement `internal/mcpserver/write.go`**

Replace the file body with:

```go
package mcpserver

import (
	"context"
	"fmt"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryWriteInput is the memory_write tool's input schema.
//
// scope/source_type/external_id are optional and default to the historical
// harness-session behavior (scope="session", source_type="harness", no
// external_id). Supplying them enables the ETL upsert path — see
// docs/plans/2026-09-06-ci-log-ingestion-design.md, "Security tradeoff": in
// Local Edition any local caller may already write any scope, so exposing
// these here does not weaken a boundary that Local Edition enforces.
type MemoryWriteInput struct {
	SessionID  string    `json:"session_id" jsonschema:"the harness-generated session identifier; required when scope is session"`
	Content    string    `json:"content" jsonschema:"the memory content to store"`
	Source     string    `json:"source" jsonschema:"identifier of the writer (harness name, or ETL source like github-actions)"`
	Tags       []string  `json:"tags,omitempty" jsonschema:"optional labels for filtering"`
	Embedding  []float32 `json:"embedding,omitempty" jsonschema:"optional precomputed embedding; if omitted the daemon generates one"`
	Scope      string    `json:"scope,omitempty" jsonschema:"session (default) or user"`
	SourceType string    `json:"source_type,omitempty" jsonschema:"harness (default) or etl"`
	ExternalID string    `json:"external_id,omitempty" jsonschema:"optional ETL key; when set the write is an idempotent upsert on (source, external_id, scope)"`
}

// MemoryWriteOutput is the memory_write tool's output: the new (or existing) entry's ID.
type MemoryWriteOutput struct {
	ID string `json:"id"`
}

func (s *Server) handleMemoryWrite(ctx context.Context, in MemoryWriteInput) (*MemoryWriteOutput, error) {
	scope := in.Scope
	if scope == "" {
		scope = "session"
	}
	if scope != "session" && scope != "user" {
		return nil, fmt.Errorf("invalid scope %q: want session or user", scope)
	}
	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = "harness"
	}
	if sourceType != "harness" && sourceType != "etl" {
		return nil, fmt.Errorf("invalid source_type %q: want harness or etl", sourceType)
	}
	sessionID := in.SessionID
	if scope == "user" {
		sessionID = "" // user-scope entries are not tied to a session
	}

	embVec := in.Embedding
	if embVec == nil {
		var err error
		embVec, err = s.embedder.Embed(ctx, in.Content)
		if err != nil {
			return nil, err
		}
	}

	entry, err := s.store.CreateMemory(store.CreateMemoryInput{
		Content:    in.Content,
		Scope:      scope,
		SessionID:  sessionID,
		Source:     in.Source,
		SourceType: sourceType,
		ExternalID: in.ExternalID,
		Tags:       in.Tags,
		Embedding:  embVec,
	})
	if err != nil {
		return nil, err
	}
	return &MemoryWriteOutput{ID: entry.ID}, nil
}
```

**Step 4: Run — expect PASS.** `go test ./internal/mcpserver/ -v`

**Step 5: check + commit**

```bash
make check
git add internal/mcpserver/write.go internal/mcpserver/write_test.go
git commit -m "feat: memory_write accepts optional scope/source_type/external_id"
```

---

### Task 5: `memory_query` structured-only path when no free-text `query`

**Context:** Today `handleMemoryQuery` always embeds `in.Query` (even `""`) and runs vector search. When the caller gives no `query` but does give `external_id` / `tags` / `source`, skip embeddings entirely and return `ListMemories` results (newest-first), preserving session isolation (own session-scope + all user-scope, never another session's).

**Files:**
- Modify: `internal/mcpserver/query.go`
- Test: `internal/mcpserver/query_test.go`

**Step 1: Failing tests**

```go
// append to internal/mcpserver/query_test.go

// countingProvider fails the test if Embed is ever called.
type countingProvider struct{ t *testing.T }

func (p countingProvider) Embed(context.Context, string) ([]float32, error) {
	p.t.Fatalf("embedder must not be called on a structured-only query")
	return nil, nil
}

func TestMemoryQuery_StructuredOnlyByExternalID(t *testing.T) {
	s := openMCPTestStore(t, 8)
	writer := New(s, embedding.NewHashProvider(8))
	if _, err := writer.handleMemoryWrite(context.Background(), MemoryWriteInput{
		Content: "cached run 1", Source: "github-actions",
		Scope: "user", SourceType: "etl", ExternalID: "o/r#1",
	}); err != nil {
		t.Fatalf("seed write error = %v", err)
	}

	reader := New(s, countingProvider{t})
	out, err := reader.handleMemoryQuery(context.Background(), MemoryQueryInput{
		SessionID: "sess-1", ExternalID: "o/r#1", Source: "github-actions", TopK: 10,
	})
	if err != nil {
		t.Fatalf("handleMemoryQuery() error = %v", err)
	}
	if len(out.Results) != 1 || out.Results[0].Content != "cached run 1" {
		t.Fatalf("Results = %+v, want the one cached entry", out.Results)
	}
}

func TestMemoryQuery_StructuredOnlyRespectsSessionIsolation(t *testing.T) {
	s := openMCPTestStore(t, 8)
	w := New(s, embedding.NewHashProvider(8))
	if _, err := w.handleMemoryWrite(context.Background(), MemoryWriteInput{
		SessionID: "sess-B", Content: "B private", Source: "hb", Tags: []string{"k:v"},
	}); err != nil {
		t.Fatalf("write error = %v", err)
	}
	r := New(s, countingProvider{t})
	out, err := r.handleMemoryQuery(context.Background(), MemoryQueryInput{
		SessionID: "sess-A", Tags: []string{"k:v"}, TopK: 10,
	})
	if err != nil {
		t.Fatalf("handleMemoryQuery() error = %v", err)
	}
	for _, e := range out.Results {
		if e.Content == "B private" {
			t.Fatal("structured query leaked another session's session-scope entry")
		}
	}
}

func TestMemoryQuery_StructuredMissIsEmptyNotError(t *testing.T) {
	s := openMCPTestStore(t, 8)
	r := New(s, countingProvider{t})
	out, err := r.handleMemoryQuery(context.Background(), MemoryQueryInput{
		SessionID: "sess-1", ExternalID: "o/r#nope", TopK: 10,
	})
	if err != nil {
		t.Fatalf("miss must not error, got %v", err)
	}
	if len(out.Results) != 0 {
		t.Fatalf("expected no results, got %+v", out.Results)
	}
}
```

**Step 2: Run — expect compile failure** (`ExternalID` not on `MemoryQueryInput`).
Run: `go test ./internal/mcpserver/ -run TestMemoryQuery -v`

**Step 3: Implement `internal/mcpserver/query.go`**

```go
package mcpserver

import (
	"context"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryQueryInput is the memory_query tool's input schema. Read-only; never a
// write path.
//
// When Query is empty the daemon runs a structured-only lookup (no embedding,
// no semantic-distance cutoff): results are the entries matching the
// external_id / tags / source filters, newest first. This is the "do I already
// have run X cached?" path — see the CI log ingestion design doc.
type MemoryQueryInput struct {
	SessionID  string   `json:"session_id" jsonschema:"the calling harness's session identifier"`
	Query      string   `json:"query,omitempty" jsonschema:"free text for semantic search; omit for an exact structured lookup"`
	Tags       []string `json:"tags,omitempty"`
	Source     string   `json:"source,omitempty"`
	ExternalID string   `json:"external_id,omitempty" jsonschema:"exact ETL key match; only honored on a structured-only query (no free text)"`
	TopK       int      `json:"top_k,omitempty"`
}

// MemoryQueryOutput is the memory_query tool's output.
type MemoryQueryOutput struct {
	Results []*store.MemoryEntry `json:"results"`
}

func (s *Server) handleMemoryQuery(ctx context.Context, in MemoryQueryInput) (*MemoryQueryOutput, error) {
	if in.TopK <= 0 {
		in.TopK = 10
	}

	if in.Query == "" {
		return s.structuredQuery(in)
	}

	embVec, err := s.embedder.Embed(ctx, in.Query)
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
	own = append(own, shared...)
	if len(own) > in.TopK {
		own = own[:in.TopK]
	}
	return &MemoryQueryOutput{Results: own}, nil
}

// structuredQuery serves the no-free-text path: exact filters, newest first,
// same session-isolation rule as the semantic path (own session scope + all
// user scope).
func (s *Server) structuredQuery(in MemoryQueryInput) (*MemoryQueryOutput, error) {
	own, err := s.store.ListMemories(store.ListFilter{
		Scope: "session", SessionID: in.SessionID,
		Tags: in.Tags, Source: in.Source, ExternalID: in.ExternalID, Limit: in.TopK,
	})
	if err != nil {
		return nil, err
	}
	shared, err := s.store.ListMemories(store.ListFilter{
		Scope: "user",
		Tags:  in.Tags, Source: in.Source, ExternalID: in.ExternalID, Limit: in.TopK,
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

**Step 4: Run — expect PASS.** `go test ./internal/mcpserver/ -v` (existing `TestMemoryQuery_SessionIsolation` still passes — it supplies `Query: "secret"`, so it takes the semantic path unchanged.)

**Step 5: check + commit**

```bash
make check
git add internal/mcpserver/query.go internal/mcpserver/query_test.go
git commit -m "feat: memory_query structured-only lookup when no free-text query"
```

---

## Phase 3 — Daemon: port file, graceful shutdown, CI-log config, cleanup ticker

### Task 6: `internal/config` — runtime dir, port/pid file paths, CI-log settings

**Files:**
- Modify: `internal/config/config.go` (`Config`, `Load`, add methods)
- Test: `internal/config/config_test.go`

**Step 1: Failing tests**

```go
// append to internal/config/config_test.go

func TestConfig_RuntimeAndFilePaths(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "/tmp/hm/hivemind.db")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RuntimeDir() != "/tmp/hm" {
		t.Errorf("RuntimeDir() = %q, want /tmp/hm", cfg.RuntimeDir())
	}
	if cfg.PortFilePath() != "/tmp/hm/daemon.port" {
		t.Errorf("PortFilePath() = %q", cfg.PortFilePath())
	}
	if cfg.PIDFilePath() != "/tmp/hm/daemon.pid" {
		t.Errorf("PIDFilePath() = %q", cfg.PIDFilePath())
	}
}

func TestConfig_CILogDefaults(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "/tmp/hm/hivemind.db")
	for _, k := range []string{"HIVEMIND_CI_LOG_DIR", "HIVEMIND_CI_LOG_MAX_AGE", "HIVEMIND_CI_LOG_MAX_SIZE", "HIVEMIND_CI_LOG_CLEANUP"} {
		t.Setenv(k, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CILogDir != "/tmp/hm/ci-logs" {
		t.Errorf("CILogDir = %q", cfg.CILogDir)
	}
	if cfg.CILogMaxAge != 30*24*time.Hour {
		t.Errorf("CILogMaxAge = %v, want 720h", cfg.CILogMaxAge)
	}
	if cfg.CILogMaxSize != 500*1024*1024 {
		t.Errorf("CILogMaxSize = %d", cfg.CILogMaxSize)
	}
	if !cfg.CILogCleanupEnabled {
		t.Error("CILogCleanupEnabled should default true")
	}
}

func TestConfig_CILogOverrides(t *testing.T) {
	t.Setenv("HIVEMIND_CI_LOG_MAX_AGE", "48h")
	t.Setenv("HIVEMIND_CI_LOG_MAX_SIZE", "1048576")
	t.Setenv("HIVEMIND_CI_LOG_CLEANUP", "off")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CILogMaxAge != 48*time.Hour {
		t.Errorf("CILogMaxAge = %v", cfg.CILogMaxAge)
	}
	if cfg.CILogMaxSize != 1<<20 {
		t.Errorf("CILogMaxSize = %d", cfg.CILogMaxSize)
	}
	if cfg.CILogCleanupEnabled {
		t.Error("HIVEMIND_CI_LOG_CLEANUP=off must disable cleanup")
	}
}
```

Add `import "time"` to the test file.

**Step 2: Run — expect compile failure.**
Run: `go test ./internal/config/ -v`

**Step 3: Implement `internal/config/config.go`**

Add imports `"path/filepath"` (already there), `"strconv"` (already there), `"strings"`, `"time"`.

Add fields to `Config`:
```go
	CILogDir            string
	CILogMaxAge         time.Duration
	CILogMaxSize        int64
	CILogCleanupEnabled bool
```

Add methods:
```go
// RuntimeDir is the directory holding the daemon's runtime files (port file,
// pid file, lock file, logs, ci-log cache) — the parent of the database file.
func (c *Config) RuntimeDir() string { return filepath.Dir(c.DataDir) }

// PortFilePath is where the daemon records the TCP port it bound.
func (c *Config) PortFilePath() string { return filepath.Join(c.RuntimeDir(), "daemon.port") }

// PIDFilePath is where the daemon records its process ID.
func (c *Config) PIDFilePath() string { return filepath.Join(c.RuntimeDir(), "daemon.pid") }
```

In `Load`, after the existing three env overrides and before `return cfg, nil`:
```go
	cfg.CILogDir = filepath.Join(cfg.RuntimeDir(), "ci-logs")
	cfg.CILogMaxAge = 30 * 24 * time.Hour
	cfg.CILogMaxSize = 500 * 1024 * 1024
	cfg.CILogCleanupEnabled = true

	if v := os.Getenv("HIVEMIND_CI_LOG_DIR"); v != "" {
		cfg.CILogDir = v
	}
	if v := os.Getenv("HIVEMIND_CI_LOG_MAX_AGE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("HIVEMIND_CI_LOG_MAX_AGE: %w", err)
		}
		cfg.CILogMaxAge = d
	}
	if v := os.Getenv("HIVEMIND_CI_LOG_MAX_SIZE"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("HIVEMIND_CI_LOG_MAX_SIZE: %w", err)
		}
		cfg.CILogMaxSize = n
	}
	if strings.EqualFold(os.Getenv("HIVEMIND_CI_LOG_CLEANUP"), "off") {
		cfg.CILogCleanupEnabled = false
	}
```

Add `"fmt"` to imports.

**Step 4: Run — expect PASS.** `go test ./internal/config/ -v`

**Step 5: check + commit**

```bash
make check
git add internal/config/
git commit -m "feat: config for runtime file paths and CI-log cache retention"
```

---

### Task 7: `internal/cilog` package — retention sweep

**Context:** New service-layer package. May import `internal/store`. Owns the CI-log tag/external-id vocabulary and the retention sweep. Raw log files live at `<CILogDir>/<owner>/<repo>/<run_id>/<name>.log`; each backing memory entry carries a `log_path:<abs path>` tag so the sweep deletes entries by walking from doomed files, not by re-parsing paths.

**Files:**
- Create: `internal/cilog/cilog.go`, `internal/cilog/sweep.go`
- Create: `internal/cilog/cilog_test.go`, `internal/cilog/sweep_test.go`
- Modify: `internal/store/memory.go` (add `DeleteMemory`)
- Test: `internal/store/memory_test.go`

**Step 1a: Failing test for `store.DeleteMemory`**

```go
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
```

**Step 1b: Implement `store.DeleteMemory`** (in `internal/store/memory.go`):

```go
// DeleteMemory removes an entry, its tags (via ON DELETE CASCADE), and its
// embedding row. Safe to call for an unknown id (no-op).
func (s *Store) DeleteMemory(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM memory_vectors WHERE rowid = (SELECT rowid FROM memory_entries WHERE id = ?)`, id,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM memory_entries WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
```

Run: `go test ./internal/store/ -run TestDeleteMemory -v` → PASS. Commit as part of this task at the end.

**Step 2: `internal/cilog/cilog.go` — vocabulary helpers + failing test**

```go
// internal/cilog/cilog_test.go
package cilog

import "testing"

func TestExternalIDs(t *testing.T) {
	if got := RunExternalID("o/r", "42"); got != "o/r#42" {
		t.Errorf("RunExternalID = %q", got)
	}
	if got := JobExternalID("o/r", "42", "99"); got != "o/r#42#99" {
		t.Errorf("JobExternalID = %q", got)
	}
}

func TestLogPathTag(t *testing.T) {
	if got := LogPathTag("/a/b.log"); got != "log_path:/a/b.log" {
		t.Errorf("LogPathTag = %q", got)
	}
	p, ok := PathFromLogPathTag("log_path:/a/b.log")
	if !ok || p != "/a/b.log" {
		t.Errorf("PathFromLogPathTag = %q, %v", p, ok)
	}
	if _, ok := PathFromLogPathTag("repo:o/r"); ok {
		t.Error("non log_path tag must not parse")
	}
}
```

```go
// internal/cilog/cilog.go

// Package cilog holds the GitHub-Actions CI-log cache convention: the
// external_id and tag vocabulary shared by the hivemind CLI and the daemon's
// retention sweep, plus the sweep itself. hivemindd's core never imports this
// as a "GitHub" concept — it only sees generic memory_write/memory_query.
package cilog

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Source is the memory `source` value for every CI-log entry.
const Source = "github-actions"

const logPathPrefix = "log_path:"

// RunExternalID is the composite key for a run-level summary entry.
func RunExternalID(repo, runID string) string { return fmt.Sprintf("%s#%s", repo, runID) }

// JobExternalID is the composite key for a per-failed-job entry.
func JobExternalID(repo, runID, jobID string) string {
	return fmt.Sprintf("%s#%s#%s", repo, runID, jobID)
}

// LogPathTag renders the tag that ties a memory entry to its raw log file.
func LogPathTag(absPath string) string { return logPathPrefix + absPath }

// PathFromLogPathTag is the inverse of LogPathTag.
func PathFromLogPathTag(tag string) (string, bool) {
	return strings.TrimPrefix(tag, logPathPrefix), strings.HasPrefix(tag, logPathPrefix)
}

// RunLogPath is where the full `gh run view --log` text for a run is cached.
func RunLogPath(dir, owner, repo, runID string) string {
	return filepath.Join(dir, owner, repo, runID, "run.log")
}

// Tags builds the standard tag set for a run-summary entry.
func Tags(repo, runID, workflow, commit, status string) []string {
	return []string{
		"repo:" + repo,
		"run_id:" + runID,
		"workflow:" + workflow,
		"commit:" + commit,
		"status:" + status,
	}
}
```

Run: `go test ./internal/cilog/ -run 'TestExternalIDs|TestLogPathTag' -v` → PASS.

**Step 3: `internal/cilog/sweep.go` — failing test first**

```go
// internal/cilog/sweep_test.go
package cilog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/causewayai/hivemind/internal/store"
)

func writeLog(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func seedEntry(t *testing.T, s *store.Store, extID, logPath string) {
	t.Helper()
	if _, err := s.CreateMemory(store.CreateMemoryInput{
		Content: "x", Scope: "user", Source: Source, SourceType: "etl",
		ExternalID: extID, Tags: []string{LogPathTag(logPath)},
		Embedding: make([]float32, 8),
	}); err != nil {
		t.Fatalf("seed error = %v", err)
	}
}

func TestSweep_DeletesByAgeAndCascadesEntries(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	old := filepath.Join(dir, "o", "r", "1", "run.log")
	fresh := filepath.Join(dir, "o", "r", "2", "run.log")
	writeLog(t, old, 10, 48*time.Hour)
	writeLog(t, fresh, 10, 1*time.Hour)
	seedEntry(t, s, "o/r#1", old)
	seedEntry(t, s, "o/r#2", fresh)

	res, err := Sweep(s, dir, 24*time.Hour, 1<<30, time.Now())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if res.FilesDeleted != 1 || res.EntriesDeleted != 1 {
		t.Fatalf("Sweep() = %+v, want 1 file + 1 entry deleted", res)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old log still present")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh log wrongly deleted")
	}
	if e, _ := s.GetMemoryByExternalID(Source, "o/r#1", "user"); e != nil {
		t.Error("entry for old log not cascaded")
	}
	if e, _ := s.GetMemoryByExternalID(Source, "o/r#2", "user"); e == nil {
		t.Error("entry for fresh log wrongly deleted")
	}
}

func TestSweep_EvictsOldestUntilUnderSizeCap(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	a := filepath.Join(dir, "o", "r", "1", "run.log") // oldest
	b := filepath.Join(dir, "o", "r", "2", "run.log")
	c := filepath.Join(dir, "o", "r", "3", "run.log") // newest
	writeLog(t, a, 100, 72*time.Hour)
	writeLog(t, b, 100, 48*time.Hour)
	writeLog(t, c, 100, 1*time.Hour)
	seedEntry(t, s, "o/r#1", a)
	seedEntry(t, s, "o/r#2", b)
	seedEntry(t, s, "o/r#3", c)

	// maxAge huge (nothing age-expires); size cap 250 -> must drop the two oldest.
	res, err := Sweep(s, dir, 365*24*time.Hour, 250, time.Now())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if res.FilesDeleted != 2 {
		t.Fatalf("Sweep() FilesDeleted = %d, want 2", res.FilesDeleted)
	}
	if _, err := os.Stat(c); err != nil {
		t.Error("newest log should survive the size sweep")
	}
}
```

**Step 4: Implement `internal/cilog/sweep.go`**

```go
package cilog

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/causewayai/hivemind/internal/store"
)

// SweepResult reports what a Sweep removed.
type SweepResult struct {
	FilesDeleted   int
	BytesDeleted   int64
	EntriesDeleted int
}

type logFile struct {
	path    string
	size    int64
	modTime time.Time
}

// Sweep enforces the retention policy on the raw-log cache rooted at dir:
// delete every *.log older than maxAge, then, if the remaining total still
// exceeds maxSize, delete oldest-first until it doesn't. For every log file
// removed it also deletes the memory entries tagged log_path:<that file>, so a
// summary/failure entry never outlives its backing log.
func Sweep(s *store.Store, dir string, maxAge time.Duration, maxSize int64, now time.Time) (SweepResult, error) {
	var res SweepResult

	var files []logFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".log" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, logFile{path: path, size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return res, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })

	cutoff := now.Add(-maxAge)
	var kept []logFile
	var keptSize int64
	for _, f := range files {
		if f.modTime.Before(cutoff) {
			if err := deleteLog(s, f, &res); err != nil {
				return res, err
			}
			continue
		}
		kept = append(kept, f)
		keptSize += f.size
	}

	for _, f := range kept {
		if keptSize <= maxSize {
			break
		}
		if err := deleteLog(s, f, &res); err != nil {
			return res, err
		}
		keptSize -= f.size
	}
	return res, nil
}

func deleteLog(s *store.Store, f logFile, res *SweepResult) error {
	entries, err := s.ListMemories(store.ListFilter{Tags: []string{LogPathTag(f.path)}})
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.DeleteMemory(e.ID); err != nil {
			return err
		}
		res.EntriesDeleted++
	}
	if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	res.FilesDeleted++
	res.BytesDeleted += f.size
	return nil
}
```

**Step 5: Run — expect PASS.**
Run: `go test ./internal/cilog/ ./internal/store/ -v`

**Step 6: check + commit**

```bash
make check
git add internal/cilog/ internal/store/memory.go internal/store/memory_test.go
git commit -m "feat: internal/cilog vocabulary + retention sweep; store.DeleteMemory"
```

---

### Task 8: Cleanup ticker

**Files:**
- Create: `internal/cilog/ticker.go`, `internal/cilog/ticker_test.go`

**Step 1: Failing test**

```go
// internal/cilog/ticker_test.go
package cilog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/causewayai/hivemind/internal/store"
)

func TestRunTicker_SweepsOnStartThenStopsOnCancel(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	old := filepath.Join(dir, "o", "r", "1", "run.log")
	writeLog(t, old, 10, 72*time.Hour)
	seedEntry(t, s, "o/r#1", old)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunTicker(ctx, s, TickerConfig{Dir: dir, MaxAge: time.Hour, MaxSize: 1 << 30, Interval: time.Hour})
		close(done)
	}()

	// The immediate startup sweep must remove the stale file well before the first tick.
	deadline := time.After(2 * time.Second)
	for {
		if _, err := statMissing(old); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("startup sweep did not run within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunTicker did not return after context cancel")
	}
}
```

Add to `sweep_test.go` (test helper reused above):
```go
func statMissing(path string) (struct{}, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return struct{}{}, nil
	}
	return struct{}{}, os.ErrExist
}
```

**Step 2: Run — expect compile failure.**
Run: `go test ./internal/cilog/ -run TestRunTicker -v`

**Step 3: Implement `internal/cilog/ticker.go`**

```go
package cilog

import (
	"context"
	"log"
	"time"

	"github.com/causewayai/hivemind/internal/store"
)

// TickerConfig parameterizes RunTicker. Interval is injectable so tests need
// not wait an hour; the daemon passes time.Hour.
type TickerConfig struct {
	Dir      string
	MaxAge   time.Duration
	MaxSize  int64
	Interval time.Duration
}

// RunTicker runs one retention Sweep immediately, then again every
// cfg.Interval, until ctx is cancelled. Sweep errors are logged, not fatal.
func RunTicker(ctx context.Context, s *store.Store, cfg TickerConfig) {
	sweep := func() {
		res, err := Sweep(s, cfg.Dir, cfg.MaxAge, cfg.MaxSize, time.Now())
		if err != nil {
			log.Printf("ci-log sweep error: %v", err)
			return
		}
		if res.FilesDeleted > 0 {
			log.Printf("ci-log sweep: removed %d files (%d bytes), %d memory entries",
				res.FilesDeleted, res.BytesDeleted, res.EntriesDeleted)
		}
	}

	sweep()
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
```

**Step 4: Run — expect PASS.** `go test ./internal/cilog/ -v`

**Step 5: check + commit**

```bash
make check
git add internal/cilog/ticker.go internal/cilog/ticker_test.go
git commit -m "feat: hourly CI-log cleanup ticker"
```

---

### Task 9: Daemon `run()` → context-cancelable `serve()`, port/pid files, graceful shutdown, ticker wiring

**Context:** `run()` currently calls `http.ListenAndServe` and blocks forever. Refactor so it: binds an explicit `net.Listener` (supporting `HIVEMIND_PORT=0` → OS-assigned), writes the real port to `cfg.PortFilePath()` and its pid to `cfg.PIDFilePath()`, starts the cleanup ticker, serves until SIGINT/SIGTERM, then shuts the HTTP server down and removes the port/pid files.

**Files:**
- Modify: `cmd/hivemindd/main.go`
- Test: `cmd/hivemindd/main_test.go`

**Step 1: Failing test**

```go
// append to cmd/hivemindd/main_test.go

func TestServe_WritesPortFileAndShutsDownCleanly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))
	t.Setenv("HIVEMIND_PORT", "0") // OS-assigned, avoids collisions
	t.Setenv("HIVEMIND_CI_LOG_CLEANUP", "off")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, cfg) }()

	portFile := cfg.PortFilePath()
	var port string
	deadline := time.After(3 * time.Second)
	for port == "" {
		select {
		case <-deadline:
			t.Fatal("port file not written within 3s")
		case <-time.After(20 * time.Millisecond):
		}
		if b, err := os.ReadFile(portFile); err == nil {
			port = strings.TrimSpace(string(b))
		}
	}

	// Daemon is reachable on the advertised port.
	resp, err := http.Post("http://127.0.0.1:"+port+"/", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	if err != nil {
		t.Fatalf("POST to daemon failed: %v", err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve() returned error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve() did not return after context cancel")
	}
	if _, err := os.Stat(portFile); !os.IsNotExist(err) {
		t.Error("port file not cleaned up on shutdown")
	}
}
```

Add imports to the test file: `"context"`, `"os"`, `"path/filepath"`, `"strings"`, `"time"`, `"net/http"`, `"github.com/causewayai/hivemind/internal/config"`.

**Step 2: Run — expect compile failure** (`serve` doesn't exist).
Run: `go test ./cmd/hivemindd/ -run TestServe -v`

**Step 3: Implement `cmd/hivemindd/main.go`**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/causewayai/hivemind/internal/config"
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/mcpserver"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("hivemindd %s (commit %s, built %s)", version, commit, date)
}

func buildMCPServer(s *store.Store, embedder embedding.Provider) *mcp.Server {
	srv := mcpserver.New(s, embedder)
	server := mcp.NewServer(&mcp.Implementation{Name: "hivemind", Version: version}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_write", Description: "Write a memory entry"}, srv.HandleMemoryWrite)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_query", Description: "Query memories via semantic search and/or structured filters"}, srv.HandleMemoryQuery)
	mcp.AddTool(server, &mcp.Tool{Name: "list_scopes", Description: "List memory scopes available in this edition"}, srv.HandleListScopes)
	return server
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println(versionString())
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := serve(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// serve runs the daemon until ctx is cancelled, then shuts down gracefully.
func serve(ctx context.Context, cfg *config.Config) error {
	s, err := store.Open(cfg.DataDir, cfg.EmbeddingDim)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = s.Close() }()

	embedder := embedding.NewHashProvider(cfg.EmbeddingDim)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return buildMCPServer(s, embedder) },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	addr := "127.0.0.1:" + strconv.Itoa(cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if err := os.MkdirAll(cfg.RuntimeDir(), 0o755); err != nil {
		return fmt.Errorf("runtime dir: %w", err)
	}
	if err := os.WriteFile(cfg.PortFilePath(), []byte(strconv.Itoa(port)), 0o644); err != nil {
		return fmt.Errorf("write port file: %w", err)
	}
	_ = os.WriteFile(cfg.PIDFilePath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer func() {
		_ = os.Remove(cfg.PortFilePath())
		_ = os.Remove(cfg.PIDFilePath())
	}()

	if cfg.CILogCleanupEnabled {
		go cilog.RunTicker(ctx, s, cilog.TickerConfig{
			Dir: cfg.CILogDir, MaxAge: cfg.CILogMaxAge, MaxSize: cfg.CILogMaxSize, Interval: time.Hour,
		})
	}

	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("hivemindd %s listening on 127.0.0.1:%d (loopback only)", version, port)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

**Step 4: Run — expect PASS.** `go test ./cmd/hivemindd/ -v` (the existing `TestVersionString` and `TestDaemon_WriteThenQuery` still pass — `TestDaemon_WriteThenQuery` builds the handler directly and is untouched).

**Step 5: check + commit**

```bash
make check
git add cmd/hivemindd/
git commit -m "feat: daemon writes port/pid files, graceful shutdown, cleanup ticker"
```

---

## Phase 4 — the `hivemind` client CLI

### Task 10: `cmd/hivemind` skeleton — arg routing + `--version`, build wiring

**Files:**
- Create: `cmd/hivemind/main.go`, `cmd/hivemind/main_test.go`
- Modify: `.gitignore`, `Makefile`

**Step 1: Failing test**

```go
// cmd/hivemind/main_test.go
package main

import "testing"

func TestVersionString(t *testing.T) {
	version, commit, date = "1.2.3", "abc1234", "2026-09-06T00:00:00Z"
	defer func() { version, commit, date = "dev", "none", "unknown" }()
	if got, want := versionString(), "hivemind 1.2.3 (commit abc1234, built 2026-09-06T00:00:00Z)"; got != want {
		t.Errorf("versionString() = %q, want %q", got, want)
	}
}

func TestDispatch_UnknownSubcommand(t *testing.T) {
	if code := dispatch([]string{"bogus"}); code == 0 {
		t.Error("unknown subcommand should exit non-zero")
	}
}
```

**Step 2: Run — expect compile failure.**
Run: `go test ./cmd/hivemind/ -v`

**Step 3: Implement `cmd/hivemind/main.go`**

```go
// Command hivemind is the client CLI for the Hivemind Local Edition daemon.
// It talks to a local hivemindd over MCP (loopback HTTP), starting one on
// demand if none is running. Its first real feature is `ci-logs`, a
// cache-first front for GitHub Actions log fetches.
package main

import (
	"fmt"
	"os"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("hivemind %s (commit %s, built %s)", version, commit, date)
}

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-version":
		fmt.Println(versionString())
		return 0
	case "ci-logs":
		return runCILogs(args[1:])
	case "daemon":
		return runDaemonCmd(args[1:])
	case "hook":
		return runHook(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hivemind: unknown subcommand %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage: hivemind <command> [args]

commands:
  ci-logs   fetch CI logs cache-first (see: hivemind ci-logs -h)
  daemon    manage the local hivemindd (start|stop|status)
  hook      Claude Code PreToolUse integration (claude|install|print)
  version   print version`
```

Add stub files so the package compiles (each returns 0 for now; real bodies land in later tasks):

```go
// cmd/hivemind/cilogs_cmd.go
package main

func runCILogs(args []string) int { panic("implemented in Task 15/16") }
```
```go
// cmd/hivemind/daemon_cmd.go
package main

func runDaemonCmd(args []string) int { panic("implemented in Task 14") }
```
```go
// cmd/hivemind/hook_cmd.go
package main

func runHook(args []string) int { panic("implemented in Task 17/18") }
```

(Using `panic` keeps `go vet`/tests honest — nothing calls them yet; `dispatch` tests only hit `version` and the unknown branch.)

**Step 4: Run — expect PASS.** `go test ./cmd/hivemind/ -v`

**Step 5: `.gitignore` + `Makefile`**

`.gitignore`: add a line `/hivemind`.

`Makefile` — replace the `build` target and add `build-cli`:

```make
build:
	CGO_ENABLED=1 go build -o hivemindd ./cmd/hivemindd
	CGO_ENABLED=1 go build -o hivemind ./cmd/hivemind
```

**Step 6: check + commit**

```bash
make check
make build   # sanity: both binaries compile
git add cmd/hivemind/ .gitignore Makefile
git commit -m "feat: hivemind CLI skeleton (arg routing, --version, build)"
```

---

### Task 11: daemon dial helper + port-file discovery

**Files:**
- Create: `cmd/hivemind/daemonconn.go`, `cmd/hivemind/daemonconn_test.go`

**Step 1: Failing test**

```go
// cmd/hivemind/daemonconn_test.go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDialDaemon_ConnectsUsingPortFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))

	s, err := store.Open(filepath.Join(dir, "hivemind.db"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(8))
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(h) // 127.0.0.1:<port>
	defer ts.Close()

	port := ts.Listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(fmt.Sprint(port)), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := dialDaemon(context.Background())
	if err != nil {
		t.Fatalf("dialDaemon() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_scopes", Arguments: map[string]any{},
	})
	if err != nil || res.IsError {
		t.Fatalf("list_scopes via dialed session failed: err=%v res=%+v", err, res)
	}
}
```

Add a tiny shared test helper `cmd/hivemind/testhelp_test.go`:
```go
package main

import (
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/mcpserver"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testMCPServer(s *store.Store, e embedding.Provider) *mcp.Server {
	srv := mcpserver.New(s, e)
	m := mcp.NewServer(&mcp.Implementation{Name: "hivemind", Version: "test"}, nil)
	mcp.AddTool(m, &mcp.Tool{Name: "memory_write"}, srv.HandleMemoryWrite)
	mcp.AddTool(m, &mcp.Tool{Name: "memory_query"}, srv.HandleMemoryQuery)
	mcp.AddTool(m, &mcp.Tool{Name: "list_scopes"}, srv.HandleListScopes)
	return m
}
```
(add missing imports `net`, `fmt` to `daemonconn_test.go`.)

**Step 2: Run — expect compile failure.**
Run: `go test ./cmd/hivemind/ -run TestDialDaemon -v`

**Step 3: Implement `cmd/hivemind/daemonconn.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/causewayai/hivemind/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runtimeDir mirrors config.Config.RuntimeDir() without opening a store: it is
// the parent of HIVEMIND_DATA_DIR, else ~/.hivemind.
func runtimeDir() string {
	if v := os.Getenv("HIVEMIND_DATA_DIR"); v != "" {
		return filepath.Dir(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".hivemind"
	}
	return filepath.Join(home, ".hivemind")
}

func portFilePath() string { return filepath.Join(runtimeDir(), "daemon.port") }
func pidFilePath() string  { return filepath.Join(runtimeDir(), "daemon.pid") }
func lockFilePath() string { return filepath.Join(runtimeDir(), "daemon.lock") }
func logFilePath() string  { return filepath.Join(runtimeDir(), "logs", "daemon.log") }

// readDaemonPort returns the port hivemindd advertised, or an error if the
// file is absent/garbage.
func readDaemonPort() (int, error) {
	b, err := os.ReadFile(portFilePath())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// dialDaemon opens an MCP session to the running daemon. It does NOT start one
// — see ensureDaemon.
func dialDaemon(ctx context.Context) (*mcp.ClientSession, error) {
	port, err := readDaemonPort()
	if err != nil {
		return nil, fmt.Errorf("daemon not discoverable: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hivemind-cli", Version: version}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d/", port),
	}, nil)
}

// daemonRunning reports whether a dial currently succeeds.
func daemonRunning(ctx context.Context) bool {
	sess, err := dialDaemon(ctx)
	if err != nil {
		return false
	}
	_ = sess.Close()
	return true
}

var _ = config.Config{} // keep the import if unused after edits; remove if lint complains
```

(Delete the last `var _ =` line and the `config` import if `go vet`/lint flags them as unused — they're only there as a reminder that `runtimeDir` must stay in sync with `config.Config.RuntimeDir`.)

**Step 4: Run — expect PASS.** `go test ./cmd/hivemind/ -v`

**Step 5: check + commit**

```bash
make check
git add cmd/hivemind/
git commit -m "feat: hivemind CLI daemon discovery + dial helper"
```

---

### Task 12: autostart — lock, spawn, readiness poll

**Files:**
- Create: `cmd/hivemind/autostart.go`, `cmd/hivemind/flock_unix.go`, `cmd/hivemind/flock_windows.go`, `cmd/hivemind/autostart_test.go`

**Step 1: Failing test** (covers the "already running → no spawn" path; the real spawn path is covered by the manual smoke test in Task 25)

```go
// cmd/hivemind/autostart_test.go
package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEnsureDaemon_ReturnsExistingWithoutSpawning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))
	// Point the daemon-binary resolver at a path that would fail if invoked.
	t.Setenv("HIVEMIND_DAEMON_BIN", filepath.Join(dir, "does-not-exist"))

	s, _ := store.Open(filepath.Join(dir, "hivemind.db"), 8)
	defer func() { _ = s.Close() }()
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(8))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(itoa(port)), 0o644)

	sess, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon() error = %v", err)
	}
	_ = sess.Close()
}

func itoa(n int) string { return fmtSprint(n) }
```

(Use `strconv.Itoa`; the `itoa` shim is just to keep the snippet import-light — replace with `strconv.Itoa` and add the import.)

**Step 2: Run — expect compile failure.**

**Step 3: Implement**

`cmd/hivemind/autostart.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ensureDaemon returns an MCP session to a running hivemindd, starting one if
// necessary. Concurrent invocations serialize on a lock file so only one
// spawns.
func ensureDaemon(ctx context.Context) (*mcp.ClientSession, error) {
	if sess, err := dialDaemon(ctx); err == nil {
		return sess, nil
	}

	unlock, err := acquireLock(lockFilePath())
	if err != nil {
		return nil, fmt.Errorf("daemon lock: %w", err)
	}
	defer unlock()

	// Someone may have started it while we waited for the lock.
	if sess, err := dialDaemon(ctx); err == nil {
		return sess, nil
	}

	bin, err := daemonBinary()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logFilePath()), 0o755); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(logFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() { _ = logf.Close() }()

	cmd := exec.Command(bin)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachAttrs() // platform-specific (flock_*.go)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hivemindd: %w", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess, err := dialDaemon(ctx); err == nil {
			return sess, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	tail, _ := os.ReadFile(logFilePath())
	return nil, fmt.Errorf("hivemindd did not become ready within 5s; recent log:\n%s", lastLines(string(tail), 20))
}

// daemonBinary resolves the hivemindd executable: $HIVEMIND_DAEMON_BIN, else a
// sibling of this binary, else PATH.
func daemonBinary() (string, error) {
	if v := os.Getenv("HIVEMIND_DAEMON_BIN"); v != "" {
		return v, nil
	}
	if self, err := os.Executable(); err == nil {
		sib := filepath.Join(filepath.Dir(self), daemonExeName())
		if _, err := os.Stat(sib); err == nil {
			return sib, nil
		}
	}
	if p, err := exec.LookPath("hivemindd"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("hivemindd not found (set HIVEMIND_DAEMON_BIN, or install it next to hivemind / on PATH)")
}

func lastLines(s string, n int) string {
	lines := splitLines(s)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return joinLines(lines)
}
```

Add small helpers `splitLines`/`joinLines` (or use `strings.Split`/`strings.Join` inline) and the `mcp` import. `daemonExeName()` lives in the platform files.

`cmd/hivemind/flock_unix.go`:

```go
//go:build !windows

package main

import (
	"os"
	"syscall"
)

func daemonExeName() string { return "hivemindd" }

func detachAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// acquireLock takes an exclusive advisory lock on path, creating it if needed.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
```

`cmd/hivemind/flock_windows.go`:

```go
//go:build windows

package main

import (
	"os"
	"syscall"
	"time"
)

func daemonExeName() string { return "hivemindd.exe" }

func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

// acquireLock: Windows has no flock; use exclusive-create with a bounded
// spin, stealing a lock file older than 30s (a crashed holder).
func acquireLock(path string) (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return func() { _ = f.Close(); _ = os.Remove(path) }, nil
		}
		if fi, statErr := os.Stat(path); statErr == nil && time.Since(fi.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
```

**Step 4: Run — expect PASS.** `go test ./cmd/hivemind/ -v`
Also `GOOS=windows go build ./cmd/hivemind` to confirm the Windows file compiles.

**Step 5: check + commit**

```bash
make check
GOOS=windows go build ./cmd/hivemind && rm -f hivemind hivemind.exe
git add cmd/hivemind/
git commit -m "feat: hivemind CLI lazily autostarts hivemindd (lock + spawn + readiness)"
```

---

### Task 13: `hivemind daemon start|stop|status`

**Files:**
- Modify: `cmd/hivemind/daemon_cmd.go`
- Create: `cmd/hivemind/daemon_cmd_test.go`

**Step 1: Failing test**

```go
// cmd/hivemind/daemon_cmd_test.go
package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDaemonStatus_RunningAndNot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))

	if code := runDaemonCmd([]string{"status"}); code == 0 {
		t.Error("status should be non-zero when no daemon is running")
	}

	s, _ := store.Open(filepath.Join(dir, "hivemind.db"), 8)
	defer func() { _ = s.Close() }()
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(8))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(strconv.Itoa(port)), 0o644)

	if code := runDaemonCmd([]string{"status"}); code != 0 {
		t.Error("status should be zero when the daemon answers")
	}
}
```

**Step 2: Run — expect panic (stub).**

**Step 3: Implement `cmd/hivemind/daemon_cmd.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func runDaemonCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: hivemind daemon <start|stop|status>")
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "start":
		sess, err := ensureDaemon(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
			return 1
		}
		_ = sess.Close()
		port, _ := readDaemonPort()
		fmt.Printf("hivemindd running on 127.0.0.1:%d\n", port)
		return 0
	case "status":
		if daemonRunning(ctx) {
			port, _ := readDaemonPort()
			fmt.Printf("running (127.0.0.1:%d)\n", port)
			return 0
		}
		fmt.Println("not running")
		return 1
	case "stop":
		return stopDaemon()
	default:
		fmt.Fprintf(os.Stderr, "hivemind daemon: unknown %q\n", args[0])
		return 2
	}
}

func stopDaemon() int {
	b, err := os.ReadFile(pidFilePath())
	if err != nil {
		fmt.Println("not running")
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: bad pid file: %v\n", err)
		return 1
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := signalTerm(proc); err != nil { // platform-specific (flock_*.go companion)
		fmt.Fprintf(os.Stderr, "hivemind: signalling hivemindd: %v\n", err)
		return 1
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(portFilePath()); os.IsNotExist(err) {
			fmt.Println("stopped")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "hivemind: hivemindd did not stop within 5s")
	return 1
}
```

Add `signalTerm` to the platform files:

`flock_unix.go`:
```go
func signalTerm(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
```
`flock_windows.go`:
```go
func signalTerm(p *os.Process) error { return p.Kill() } // no SIGTERM on Windows
```

**Step 4: Run — expect PASS.** `go test ./cmd/hivemind/ -v`

**Step 5: check + commit**

```bash
make check
git add cmd/hivemind/
git commit -m "feat: hivemind daemon start|stop|status"
```

---

### Task 14: version-skew check (refuse + restart an older daemon)

**Files:**
- Create: `cmd/hivemind/versionskew.go`, `cmd/hivemind/versionskew_test.go`
- Modify: `cmd/hivemind/daemonconn.go` (`connectChecked` wrapper) and callers

**Step 1: Failing test (pure comparison)**

```go
// cmd/hivemind/versionskew_test.go
package main

import "testing"

func TestDaemonOlderThanCLI(t *testing.T) {
	cases := []struct {
		cli, daemon string
		older       bool
	}{
		{"1.2.0", "1.1.9", true},
		{"1.2.0", "1.2.0", false},
		{"1.2.0", "1.3.0", false},
		{"dev", "1.0.0", false},   // local dev: never refuse
		{"1.0.0", "dev", false},   // daemon dev build: never refuse
		{"1.2.0", "garbage", false},
	}
	for _, c := range cases {
		if got := daemonOlderThanCLI(c.cli, c.daemon); got != c.older {
			t.Errorf("daemonOlderThanCLI(%q,%q) = %v, want %v", c.cli, c.daemon, got, c.older)
		}
	}
}
```

**Step 2: Run — expect compile failure.**

**Step 3: Implement `cmd/hivemind/versionskew.go`**

```go
package main

import (
	"strconv"
	"strings"
)

// daemonOlderThanCLI reports whether the daemon's semver is strictly older
// than the CLI's. Non-semver on either side (e.g. "dev") disables the check.
func daemonOlderThanCLI(cli, daemon string) bool {
	c, ok1 := parseSemver(cli)
	d, ok2 := parseSemver(daemon)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if d[i] != c[i] {
			return d[i] < c[i]
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
```

**Step 4: wire it in.** In `daemonconn.go` add:

```go
// serverVersion returns the daemon's advertised implementation version.
func serverVersion(sess *mcp.ClientSession) string {
	if init := sess.InitializeResult(); init != nil && init.ServerInfo != nil {
		return init.ServerInfo.Version
	}
	return ""
}
```

(Check the exact accessor against the SDK — `go doc github.com/modelcontextprotocol/go-sdk/mcp.ClientSession`. If it exposes the initialize result differently, adapt; the daemon sets `Version: version` in `buildMCPServer` per Task 9.)

Add a `connectChecked` used by `ci-logs` and `daemon start`:

```go
// connectChecked returns a session, restarting the daemon once if it is older
// than this CLI.
func connectChecked(ctx context.Context) (*mcp.ClientSession, error) {
	sess, err := ensureDaemon(ctx)
	if err != nil {
		return nil, err
	}
	if daemonOlderThanCLI(version, serverVersion(sess)) {
		_ = sess.Close()
		fmt.Fprintf(os.Stderr, "hivemind: running hivemindd %s is older than CLI %s; restarting it\n",
			serverVersion(sess), version)
		if code := stopDaemon(); code != 0 {
			return nil, fmt.Errorf("could not restart the older daemon automatically; stop it yourself (e.g. `brew services restart hivemindd`) and retry")
		}
		return ensureDaemon(ctx)
	}
	return sess, nil
}
```

Point `runCILogs` and `daemon start` at `connectChecked` instead of `ensureDaemon`.

**Step 5: Run — expect PASS.** `go test ./cmd/hivemind/ -v`

**Step 6: check + commit**

```bash
make check
git add cmd/hivemind/
git commit -m "feat: hivemind refuses + restarts an older hivemindd on version skew"
```

---

### Task 15: `hivemind ci-logs` — arg parse, repo resolution, cache-hit path

**Scope note (from the design doc's resolved decisions):** the CLI mirrors `gh`'s argument shape so the hook rewrite is near-literal. For this POC implement one form:

```
hivemind ci-logs run view <run-id> [-R|--repo <owner/repo>] [--log | --log-failed]
```

`--log`/`--log-failed` are accepted and (on a miss) forwarded to `gh`; on a hit they select run-summary+failures vs. failures-only from cache. Repo resolves from `-R`, else `gh repo view --json nameWithOwner -q .nameWithOwner`, else `git remote get-url origin` parsing.

**Files:**
- Modify: `cmd/hivemind/cilogs_cmd.go`
- Create: `cmd/hivemind/cilogs_cmd_test.go`
- Create: `internal/cilog/parse.go`, `internal/cilog/parse_test.go` (repo-from-remote parsing — pure, unit-tested)

**Step 1: Failing tests**

`internal/cilog/parse_test.go`:
```go
package cilog

import "testing"

func TestRepoFromRemoteURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"git@github.com:causewayai/hivemind.git", "causewayai/hivemind"},
		{"https://github.com/causewayai/hivemind.git", "causewayai/hivemind"},
		{"https://github.com/causewayai/hivemind", "causewayai/hivemind"},
	} {
		if got, _ := RepoFromRemoteURL(c.in); got != c.want {
			t.Errorf("RepoFromRemoteURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, ok := RepoFromRemoteURL("file:///tmp/x"); ok {
		t.Error("non-github remote should not parse")
	}
}
```

`cmd/hivemind/cilogs_cmd_test.go` (cache-hit path, `gh` runner injected so it must not be called):
```go
package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCILogs_CacheHitPrintsFromCacheNoGH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))

	s, _ := store.Open(filepath.Join(dir, "hivemind.db"), 768)
	defer func() { _ = s.Close() }()
	// Seed a run-summary entry as the daemon would have.
	_, _ = s.CreateMemory(store.CreateMemoryInput{
		Content: "RUN 42 conclusion=failure workflow=CI", Scope: "user",
		Source: cilog.Source, SourceType: "etl",
		ExternalID: cilog.RunExternalID("causewayai/hivemind", "42"),
		Tags:       cilog.Tags("causewayai/hivemind", "42", "CI", "abc123", "fail"),
		Embedding:  make([]float32, 768),
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(768))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(strconv.Itoa(port)), 0o644)

	var out bytes.Buffer
	ghCalled := false
	code := runCILogsWith(context.Background(), cilogsDeps{
		stdout: &out,
		gh:     func(context.Context, ...string) ([]byte, error) { ghCalled = true; return nil, nil },
	}, []string{"run", "view", "42", "-R", "causewayai/hivemind", "--log"})

	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if ghCalled {
		t.Fatal("gh must not be called on a cache hit")
	}
	if !bytes.Contains(out.Bytes(), []byte("RUN 42 conclusion=failure")) {
		t.Fatalf("cache-hit output missing summary; got:\n%s", out.String())
	}
}
```

**Step 2: Run — expect compile failure.**

**Step 3: Implement**

`internal/cilog/parse.go`:
```go
package cilog

import (
	"regexp"
	"strings"
)

var remoteRe = regexp.MustCompile(`github\.com[:/]+([^/]+/[^/]+?)(?:\.git)?/?$`)

// RepoFromRemoteURL extracts "owner/repo" from a github.com git remote URL.
func RepoFromRemoteURL(url string) (string, bool) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", false
	}
	return m[1], true
}
```

`cmd/hivemind/cilogs_cmd.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type cilogsDeps struct {
	stdout io.Writer
	gh     func(ctx context.Context, args ...string) ([]byte, error)
}

func defaultCILogsDeps() cilogsDeps {
	return cilogsDeps{
		stdout: os.Stdout,
		gh: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "gh", args...).Output()
		},
	}
}

func runCILogs(args []string) int {
	return runCILogsWith(context.Background(), defaultCILogsDeps(), args)
}

type cilogsArgs struct {
	runID     string
	repo      string
	logFailed bool
	logAll    bool
}

func parseCILogsArgs(args []string) (cilogsArgs, error) {
	// Expect: run view <run-id> [flags]
	if len(args) < 3 || args[0] != "run" || args[1] != "view" {
		return cilogsArgs{}, fmt.Errorf("usage: hivemind ci-logs run view <run-id> [-R owner/repo] [--log|--log-failed]")
	}
	var a cilogsArgs
	a.runID = args[2]
	fs := flag.NewFlagSet("ci-logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.repo, "R", "", "")
	fs.StringVar(&a.repo, "repo", "", "")
	fs.BoolVar(&a.logAll, "log", false, "")
	fs.BoolVar(&a.logFailed, "log-failed", false, "")
	if err := fs.Parse(args[3:]); err != nil {
		return cilogsArgs{}, err
	}
	return a, nil
}

func resolveRepo(ctx context.Context, d cilogsDeps, flagRepo string) (string, error) {
	if flagRepo != "" {
		return flagRepo, nil
	}
	if out, err := d.gh(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			return r, nil
		}
	}
	if out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output(); err == nil {
		if r, ok := cilog.RepoFromRemoteURL(string(out)); ok {
			return r, nil
		}
	}
	return "", fmt.Errorf("could not determine repo; pass -R owner/repo")
}

func runCILogsWith(ctx context.Context, d cilogsDeps, args []string) int {
	a, err := parseCILogsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 2
	}
	repo, err := resolveRepo(ctx, d, a.repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 2
	}

	sess, err := connectChecked(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	defer func() { _ = sess.Close() }()

	extID := cilog.RunExternalID(repo, a.runID)
	hits, err := queryByExternalID(ctx, sess, extID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: cache query failed: %v\n", err)
		return 1
	}
	if len(hits) > 0 {
		for _, h := range hits {
			fmt.Fprintln(d.stdout, h)
		}
		return 0
	}

	return cacheMiss(ctx, d, sess, repo, a) // Task 16
}

// queryByExternalID runs a structured-only memory_query and returns entry contents.
func queryByExternalID(ctx context.Context, sess *mcp.ClientSession, extID string) ([]string, error) {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_query",
		Arguments: map[string]any{
			"session_id":  "hivemind-cli",
			"external_id": extID,
			"source":      cilog.Source,
			"top_k":       50,
		},
	})
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("memory_query returned an error result")
	}
	var out struct {
		Results []struct {
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(mustJSON(res.StructuredContent), &out); err != nil {
		return nil, err
	}
	contents := make([]string, 0, len(out.Results))
	for _, r := range out.Results {
		contents = append(contents, r.Content)
	}
	return contents, nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
```

Delete the Task 10 stub `runCILogs` in `cilogs_cmd.go` (this file now defines it).

**Step 4: Run — expect PASS** for `TestCILogs_CacheHitPrintsFromCacheNoGH` and the `internal/cilog` parse test. `cacheMiss` can be a temporary `return 1` stub with a `// Task 16` comment so the package compiles; the hit-path test doesn't reach it.

Run: `go test ./internal/cilog/ ./cmd/hivemind/ -v`

**Step 5: check + commit**

```bash
make check
git add cmd/hivemind/ internal/cilog/parse.go internal/cilog/parse_test.go
git commit -m "feat: hivemind ci-logs run view — arg parsing, repo resolution, cache-hit path"
```

---

### Task 16: `hivemind ci-logs` — cache-miss path (fetch via `gh`, populate cache)

**Files:**
- Modify: `cmd/hivemind/cilogs_cmd.go` (`cacheMiss`)
- Create: `internal/cilog/summary.go`, `internal/cilog/summary_test.go` (pure builders)
- Test: `cmd/hivemind/cilogs_cmd_test.go`

**Step 1: Failing tests**

`internal/cilog/summary_test.go`:
```go
package cilog

import (
	"strings"
	"testing"
)

func TestBuildRunSummary(t *testing.T) {
	m := RunMeta{
		Repo: "o/r", RunID: "42", WorkflowName: "CI", Conclusion: "failure",
		HeadSHA: "abc123", HeadBranch: "main", Event: "push",
		Jobs: []JobMeta{{DatabaseID: 7, Name: "build", Conclusion: "failure"}, {DatabaseID: 8, Name: "lint", Conclusion: "success"}},
	}
	got := BuildRunSummary(m)
	for _, want := range []string{"o/r", "run 42", "CI", "failure", "abc123", "main", "push", "build"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lint") {
		t.Errorf("successful job should not be listed as failed:\n%s", got)
	}
}

func TestExtractJobFailure(t *testing.T) {
	log := strings.Join([]string{
		"build\t2026-09-06T00:00:01Z line one",
		"build\t2026-09-06T00:00:02Z ##[error]zip: command not found",
		"build\t2026-09-06T00:00:03Z after error",
		"lint\t2026-09-06T00:00:01Z unrelated",
	}, "\n")
	got := ExtractJobFailure(log, "build")
	if !strings.Contains(got, "zip: command not found") {
		t.Errorf("expected the error line, got:\n%s", got)
	}
	if strings.Contains(got, "unrelated") {
		t.Errorf("must not include other jobs' lines:\n%s", got)
	}
}
```

`cmd/hivemind/cilogs_cmd_test.go` (append):
```go
func TestCILogs_CacheMissFetchesAndPopulates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVEMIND_DATA_DIR", filepath.Join(dir, "hivemind.db"))
	t.Setenv("HIVEMIND_CI_LOG_DIR", filepath.Join(dir, "ci-logs"))

	s, _ := store.Open(filepath.Join(dir, "hivemind.db"), 768)
	defer func() { _ = s.Close() }()
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testMCPServer(s, embedding.NewHashProvider(768))
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(filepath.Join(dir, "daemon.port"), []byte(strconv.Itoa(port)), 0o644)

	logText := "build\t2026-09-06T00:00:02Z ##[error]boom\n"
	metaJSON := `{"workflowName":"CI","conclusion":"failure","headSha":"abc","headBranch":"main","event":"push","jobs":[{"databaseId":7,"name":"build","conclusion":"failure"}]}`

	var out bytes.Buffer
	code := runCILogsWith(context.Background(), cilogsDeps{
		stdout: &out,
		gh: func(_ context.Context, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "--json"):
				return []byte(metaJSON), nil
			case strings.Contains(joined, "--log"):
				return []byte(logText), nil
			case strings.Contains(joined, "repo view"):
				return []byte("causewayai/hivemind\n"), nil
			}
			return nil, nil
		},
	}, []string{"run", "view", "42", "-R", "causewayai/hivemind", "--log"})

	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	// (a) gh's log text is passed through to stdout unchanged
	if !strings.Contains(out.String(), "##[error]boom") {
		t.Errorf("stdout should echo the gh --log output; got:\n%s", out.String())
	}
	// (b) raw log written to the cache dir
	runLog := cilog.RunLogPath(filepath.Join(dir, "ci-logs"), "causewayai", "hivemind", "42")
	if _, err := os.Stat(runLog); err != nil {
		t.Errorf("run.log not written: %v", err)
	}
	// (c) a summary entry + a failure entry are now queryable
	sum, _ := s.GetMemoryByExternalID(cilog.Source, cilog.RunExternalID("causewayai/hivemind", "42"), "user")
	if sum == nil {
		t.Error("run-summary entry not written")
	}
	fail, _ := s.GetMemoryByExternalID(cilog.Source, cilog.JobExternalID("causewayai/hivemind", "42", "7"), "user")
	if fail == nil {
		t.Error("job-failure entry not written")
	}
}
```

**Step 2: Run — expect failure** (`cacheMiss` stub returns 1; builders missing).

**Step 3: Implement `internal/cilog/summary.go`**

```go
package cilog

import (
	"fmt"
	"strings"
)

// RunMeta is the subset of `gh run view --json ...` this cache needs.
type RunMeta struct {
	Repo         string    `json:"-"`
	RunID        string    `json:"-"`
	WorkflowName string    `json:"workflowName"`
	Conclusion   string    `json:"conclusion"`
	HeadSHA      string    `json:"headSha"`
	HeadBranch   string    `json:"headBranch"`
	Event        string    `json:"event"`
	Jobs         []JobMeta `json:"jobs"`
}

// JobMeta is one job from `gh run view --json jobs`.
type JobMeta struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// FailedJobs returns only the jobs whose conclusion is not "success".
func (m RunMeta) FailedJobs() []JobMeta {
	var out []JobMeta
	for _, j := range m.Jobs {
		if j.Conclusion != "" && j.Conclusion != "success" {
			out = append(out, j)
		}
	}
	return out
}

// StatusWord is "pass" or "fail" for the tag vocabulary.
func (m RunMeta) StatusWord() string {
	if m.Conclusion == "success" {
		return "pass"
	}
	return "fail"
}

// BuildRunSummary renders the run-level summary entry's content.
func BuildRunSummary(m RunMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s run %s — %s\n", m.Repo, m.RunID, m.Conclusion)
	fmt.Fprintf(&b, "workflow: %s\nbranch: %s\ncommit: %s\nevent: %s\n",
		m.WorkflowName, m.HeadBranch, m.HeadSHA, m.Event)
	if fj := m.FailedJobs(); len(fj) > 0 {
		names := make([]string, len(fj))
		for i, j := range fj {
			names[i] = j.Name
		}
		fmt.Fprintf(&b, "failed jobs: %s\n", strings.Join(names, ", "))
	}
	return b.String()
}

// ExtractJobFailure pulls the lines belonging to job jobName out of a
// `gh run view --log` blob (whose lines are "<job>\t<timestamp> <text>"),
// trimmed to a window around the first error marker.
func ExtractJobFailure(log, jobName string) string {
	var lines []string
	for _, ln := range strings.Split(log, "\n") {
		if strings.HasPrefix(ln, jobName+"\t") {
			lines = append(lines, ln)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	errIdx := -1
	for i, ln := range lines {
		if strings.Contains(ln, "##[error]") || strings.Contains(strings.ToLower(ln), "error") {
			errIdx = i
			break
		}
	}
	if errIdx < 0 {
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		return strings.Join(lines, "\n")
	}
	lo, hi := errIdx-10, errIdx+10
	if lo < 0 {
		lo = 0
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	return strings.Join(lines[lo:hi], "\n")
}
```

**Step 4: Implement `cacheMiss` in `cmd/hivemind/cilogs_cmd.go`**

```go
func cacheMiss(ctx context.Context, d cilogsDeps, sess *mcp.ClientSession, repo string, a cilogsArgs) int {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		fmt.Fprintf(os.Stderr, "hivemind: bad repo %q\n", repo)
		return 2
	}

	logFlag := "--log"
	if a.logFailed {
		logFlag = "--log-failed"
	}
	logOut, err := d.gh(ctx, "run", "view", a.runID, "-R", repo, logFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: gh log fetch failed: %v\n", err)
		return 1
	}
	metaOut, err := d.gh(ctx, "run", "view", a.runID, "-R", repo, "--json",
		"workflowName,conclusion,headSha,headBranch,event,jobs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: gh metadata fetch failed: %v\n", err)
		return 1
	}
	var meta cilog.RunMeta
	if err := json.Unmarshal(metaOut, &meta); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: parsing gh metadata: %v\n", err)
		return 1
	}
	meta.Repo, meta.RunID = repo, a.runID

	cacheDir := os.Getenv("HIVEMIND_CI_LOG_DIR")
	if cacheDir == "" {
		cacheDir = filepath.Join(runtimeDir(), "ci-logs")
	}
	runLog := cilog.RunLogPath(cacheDir, owner, name, a.runID)
	if err := os.MkdirAll(filepath.Dir(runLog), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := os.WriteFile(runLog, logOut, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}

	// Run-summary entry.
	if err := writeEntry(ctx, sess, writeArgs{
		content:    cilog.BuildRunSummary(meta),
		externalID: cilog.RunExternalID(repo, a.runID),
		tags:       append(cilog.Tags(repo, a.runID, meta.WorkflowName, meta.HeadSHA, meta.StatusWord()), cilog.LogPathTag(runLog)),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: caching summary: %v\n", err)
		return 1
	}
	// One entry per failed job.
	for _, j := range meta.FailedJobs() {
		body := cilog.ExtractJobFailure(string(logOut), j.Name)
		if body == "" {
			body = fmt.Sprintf("job %q failed (no error lines isolated; see %s)", j.Name, runLog)
		}
		if err := writeEntry(ctx, sess, writeArgs{
			content:    body,
			externalID: cilog.JobExternalID(repo, a.runID, itoa64(j.DatabaseID)),
			tags: []string{
				"repo:" + repo, "run_id:" + a.runID, "job:" + j.Name,
				"status:fail", cilog.LogPathTag(runLog),
			},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: caching job failure: %v\n", err)
			return 1
		}
	}

	// Transparent passthrough: emit exactly what `gh` produced.
	_, _ = d.stdout.Write(logOut)
	return 0
}

type writeArgs struct {
	content    string
	externalID string
	tags       []string
}

func writeEntry(ctx context.Context, sess *mcp.ClientSession, w writeArgs) error {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_write",
		Arguments: map[string]any{
			"content":     w.content,
			"source":      cilog.Source,
			"scope":       "user",
			"source_type": "etl",
			"external_id": w.externalID,
			"tags":        w.tags,
		},
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("memory_write error result")
	}
	return nil
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
```

Add imports: `"path/filepath"`, `"strconv"`.

**Step 5: Run — expect PASS.** `go test ./internal/cilog/ ./cmd/hivemind/ -v`

**Step 6: check + commit**

```bash
make check
git add cmd/hivemind/ internal/cilog/summary.go internal/cilog/summary_test.go
git commit -m "feat: hivemind ci-logs cache-miss — gh fetch, raw log cache, summary + failure entries"
```

---

## Phase 5 — Claude Code hook, rules snippet, packaging, docs

### Task 17: `internal/cilog` command matcher + `hivemind hook claude`

**Context:** The transparent-rewrite hook. `hivemind hook claude` reads a `PreToolUse` JSON payload on stdin and, when `tool_input.command` is a CI-log fetch, prints the rewrite response modeled exactly on `../rtk/src/hooks/hook_cmd.rs` `process_claude_payload_from_decision`. Match logic is pure Go in `internal/cilog` so it's unit-tested without shell.

**Files:**
- Create: `internal/cilog/rewrite.go`, `internal/cilog/rewrite_test.go`
- Modify: `cmd/hivemind/hook_cmd.go`
- Create: `cmd/hivemind/hook_cmd_test.go`

**Step 1: Failing tests**

`internal/cilog/rewrite_test.go`:
```go
package cilog

import "testing"

func TestRewriteGHLogCommand(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		matched bool
	}{
		{`gh run view 42 --log -R o/r`, `hivemind ci-logs run view 42 --log -R o/r`, true},
		{`gh run view --log-failed 42`, `hivemind ci-logs run view --log-failed 42`, true},
		{`gh run view 42 --json conclusion`, ``, false},        // no --log: leave alone
		{`gh run list`, ``, false},                             // not a log fetch
		{`gh pr view 3`, ``, false},                            // unrelated
		{`echo gh run view 42 --log`, ``, false},               // not the leading command
		{`gh run view 42 --log && rm -rf /`, ``, false},        // compound: refuse to launder
		{`gh run view 42 --log | tee x`, ``, false},            // pipeline: leave alone
		{`hivemind ci-logs run view 42 --log`, ``, false},      // already rewritten
	}
	for _, c := range cases {
		got, ok := RewriteGHLogCommand(c.in)
		if ok != c.matched || got != c.want {
			t.Errorf("RewriteGHLogCommand(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.matched)
		}
	}
}
```

`cmd/hivemind/hook_cmd_test.go`:
```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHookClaude_RewritesLogFetch(t *testing.T) {
	in := `{"tool_name":"Bash","tool_input":{"command":"gh run view 42 --log -R o/r","description":"logs"}}`
	var out bytes.Buffer
	code := runHookClaude(strings.NewReader(in), &out)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var resp struct {
		HookSpecificOutput struct {
			UpdatedInput map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out.String())
	}
	if resp.HookSpecificOutput.UpdatedInput["command"] != "hivemind ci-logs run view 42 --log -R o/r" {
		t.Fatalf("wrong rewrite: %v", resp.HookSpecificOutput.UpdatedInput)
	}
	if resp.HookSpecificOutput.UpdatedInput["description"] != "logs" {
		t.Fatalf("other tool_input fields must be preserved: %v", resp.HookSpecificOutput.UpdatedInput)
	}
}

func TestHookClaude_PassthroughEmitsNothing(t *testing.T) {
	var out bytes.Buffer
	code := runHookClaude(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"gh run list"}}`), &out)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("passthrough must emit nothing; code=%d out=%q", code, out.String())
	}
}
```

**Step 2: Run — expect compile failure.**

**Step 3: Implement `internal/cilog/rewrite.go`**

```go
package cilog

import "strings"

// unsafeShell reports constructs we refuse to rewrite around, so the hook
// never "launders" a compound command into an auto-allowed rewrite. Mirrors
// rtk's contains_unattestable_construct, simplified.
func unsafeShell(cmd string) bool {
	for _, tok := range []string{"&&", "||", "|", ";", "\n", "$(", "`", ">", "<", "&"} {
		if strings.Contains(cmd, tok) {
			return true
		}
	}
	return false
}

// RewriteGHLogCommand returns the `hivemind ci-logs …` equivalent of a
// `gh run view … --log[-failed] …` command, and true, when cmd is exactly
// such a fetch (leading command, no shell metacharacters). Otherwise ("", false).
func RewriteGHLogCommand(cmd string) (string, bool) {
	trimmed := strings.TrimSpace(cmd)
	if unsafeShell(trimmed) {
		return "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 3 || fields[0] != "gh" || fields[1] != "run" || fields[2] != "view" {
		return "", false
	}
	hasLog := false
	for _, f := range fields[3:] {
		if f == "--log" || f == "--log-failed" {
			hasLog = true
			break
		}
	}
	if !hasLog {
		return "", false
	}
	return "hivemind ci-logs run view " + strings.Join(fields[3:], " "), true
}
```

**Step 4: Implement `cmd/hivemind/hook_cmd.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/causewayai/hivemind/internal/cilog"
)

func runHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: hivemind hook <claude|install|print>")
		return 2
	}
	switch args[0] {
	case "claude":
		return runHookClaude(os.Stdin, os.Stdout)
	case "install":
		return runHookInstall(args[1:]) // Task 18
	case "print":
		fmt.Println(hookSettingsSnippet())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hivemind hook: unknown %q\n", args[0])
		return 2
	}
}

// runHookClaude implements the Claude Code PreToolUse contract: read the
// payload, and if tool_input.command is a gh log fetch, emit a
// hookSpecificOutput.updatedInput rewrite. Any other input: emit nothing,
// exit 0 (Claude Code proceeds with the original command).
func runHookClaude(r io.Reader, w io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hivemind hook] read stdin: %v\n", err)
		return 0
	}
	var payload struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "[hivemind hook] parse: %v\n", err)
		return 0
	}
	if payload.ToolName != "Bash" && payload.ToolName != "bash" {
		return 0
	}
	cmd, _ := payload.ToolInput["command"].(string)
	rewritten, ok := cilog.RewriteGHLogCommand(cmd)
	if !ok {
		return 0
	}
	updated := make(map[string]any, len(payload.ToolInput))
	for k, v := range payload.ToolInput {
		updated[k] = v
	}
	updated["command"] = rewritten

	resp := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecisionReason": "hivemind CI-log cache: served from local cache or fetched+cached once",
			"updatedInput":             updated,
		},
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "[hivemind hook] encode: %v\n", err)
		return 0
	}
	return 0
}
```

Remove the Task 10 `hook_cmd.go` stub (this replaces it).

**Step 5: Run — expect PASS.** `go test ./internal/cilog/ ./cmd/hivemind/ -v`

**Step 6: check + commit**

```bash
make check
git add internal/cilog/rewrite.go internal/cilog/rewrite_test.go cmd/hivemind/hook_cmd.go cmd/hivemind/hook_cmd_test.go
git commit -m "feat: hivemind hook claude — transparent PreToolUse rewrite of gh log fetches"
```

---

### Task 18: `hivemind hook install` / `hivemind hook print`

**Files:**
- Create: `cmd/hivemind/hook_install.go`, `cmd/hivemind/hook_install_test.go`

**Step 1: Failing tests**

```go
// cmd/hivemind/hook_install_test.go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHookInstall_IdempotentAndPreservesKeys(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet","hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"other"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runHookInstall([]string{"--settings", settings}); code != 0 {
		t.Fatalf("install exit = %d", code)
	}
	if code := runHookInstall([]string{"--settings", settings}); code != 0 {
		t.Fatalf("second install exit = %d", code)
	}

	m := readJSON(t, settings)
	if m["model"] != "sonnet" {
		t.Error("unrelated key 'model' was dropped")
	}
	pre := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	hivemindEntries := 0
	for _, e := range pre {
		hooks := e.(map[string]any)["hooks"].([]any)
		for _, h := range hooks {
			if h.(map[string]any)["command"] == "hivemind hook claude" {
				hivemindEntries++
			}
		}
	}
	if hivemindEntries != 1 {
		t.Fatalf("want exactly 1 hivemind hook entry after 2 installs, got %d", hivemindEntries)
	}
	if len(pre) < 2 {
		t.Error("pre-existing Read matcher entry was lost")
	}
}
```

**Step 2: Run — expect compile failure.**

**Step 3: Implement `cmd/hivemind/hook_install.go`**

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const hookCommand = "hivemind hook claude"

func hookSettingsSnippet() string {
	return `{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "hivemind hook claude" } ] }
    ]
  }
}`
}

func defaultClaudeSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func runHookInstall(args []string) int {
	fs := flag.NewFlagSet("hook install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("settings", defaultClaudeSettingsPath(), "path to Claude Code settings.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "hivemind: cannot locate ~/.claude/settings.json; pass --settings")
		return 1
	}

	root := map[string]any{}
	if b, err := os.ReadFile(*path); err == nil {
		if err := json.Unmarshal(b, &root); err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: %s is not valid JSON: %v\n", *path, err)
			return 1
		}
	}

	if hookAlreadyInstalled(root) {
		fmt.Printf("hivemind hook already present in %s\n", *path)
		return 0
	}
	insertHook(root)

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*path, append(out, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	fmt.Printf("installed hivemind PreToolUse hook into %s\n", *path)
	return 0
}

func hookAlreadyInstalled(root map[string]any) bool {
	hooks, _ := root["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	for _, e := range pre {
		em, _ := e.(map[string]any)
		hs, _ := em["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if hm["command"] == hookCommand {
				return true
			}
		}
	}
	return false
}

func insertHook(root map[string]any) {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	pre, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(pre, map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": hookCommand},
		},
	})
}
```

**Step 4: Run — expect PASS.** `go test ./cmd/hivemind/ -v`

**Step 5: check + commit**

```bash
make check
git add cmd/hivemind/
git commit -m "feat: hivemind hook install / print (Claude Code settings.json)"
```

---

### Task 19: generic rules snippet for hookless harnesses

**Files:**
- Create: `docs/ci-log-cache-rules.md`

No tests (doc). Content:

```markdown
# CI log cache — rules snippet for hookless harnesses

Paste into your harness's rules file (Cursor `.cursor/rules`, `.windsurfrules`,
`.clinerules`, etc.). Claude Code users should run `hivemind hook install`
instead — it does this transparently.

---

Before fetching GitHub Actions logs, check the local hivemind cache first.

Instead of:

    gh run view <run-id> --log [-R owner/repo]

run:

    hivemind ci-logs run view <run-id> --log [-R owner/repo]

`hivemind ci-logs` returns the cached run summary and per-failed-job error
text if the run was fetched before (this session or a past one), and
otherwise runs the real `gh` fetch, prints its output unchanged, and
populates the cache for next time. It starts the local `hivemindd` on demand;
no setup beyond `brew install hivemindd hivemind` (or the tarball).
```

**Commit:**

```bash
git add docs/ci-log-cache-rules.md
git commit -m "docs: rules snippet for routing CI-log fetches through hivemind"
```

---

### Task 20: Makefile + CI wiring for the second binary

**Files:**
- Modify: `Makefile` (already did `build` in Task 10 — verify), `.github/workflows/ci.yml`

**Step 1:** `.github/workflows/ci.yml` — the `make check` step runs `go vet ./...` + `golangci-lint run ./...` + `go test ./...`, which already compile and test `./cmd/hivemind`. Add an explicit build of both binaries so a link/cgo break on any platform fails CI early. After the `make check` step:

```yaml
      - name: Build binaries
        run: make build
        shell: bash
```

**Step 2:** Run locally: `make check && make build`. Expected: both succeed, `./hivemind` and `./hivemindd` produced.

**Step 3: commit**

```bash
rm -f hivemind hivemindd
git add .github/workflows/ci.yml Makefile
git commit -m "chore: build both binaries in CI"
```

---

### Task 21: Release workflow — build, archive, publish `hivemind` alongside `hivemindd`

**Files:**
- Modify: `.github/workflows/release.yml`

**Changes (mirror every place `hivemindd` is handled):**

1. **Build step** — after the existing `go build … -o "hivemindd${EXT}" ./cmd/hivemindd`, add:
```bash
          go build -ldflags "-X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
            -o "hivemind${EXT}" ./cmd/hivemind
```

2. **Archive (tar.gz)** — `tar -czf "hivemind_${{ matrix.goos }}_${{ matrix.goarch }}.tar.gz" hivemindd hivemind` — put **both** binaries in one archive per platform (rename the archive base from `hivemindd_` to `hivemind_` and update the artifact `name:`/`path:` globs). Do the same for the `zip` step (`Compress-Archive -Path hivemindd.exe, hivemind.exe`).

3. **`publish` job** — `checksums.txt`, the per-asset SHA extraction (`grep`), and `gh release create dist/*` all key off the archive base name; update `hivemindd_` → `hivemind_` consistently.

4. **Homebrew formula** (`tap/Formula/hivemindd.rb`) — rename to `hivemind.rb`, class `Hivemind`, and:
```ruby
   def install
     bin.install "hivemindd"
     bin.install "hivemind"
   end
```
Keep the `service do … run [opt_bin/"hivemindd"] … end` block (the service is still `hivemindd`). Update the `test do` block to also assert `hivemind --version`.

5. **Scoop manifest** (`bucket/bucket/hivemindd.json`) — rename to `hivemind.json`, `"bin": ["hivemindd.exe", "hivemind.exe"]`.

6. Commit message on the tap/bucket push: `hivemind ${VERSION}`.

**No automated test** — this only runs on a `v*.*.*` tag. Verification is Task 25's manual release rehearsal or the next real release.

**Commit:**

```bash
git add .github/workflows/release.yml
git commit -m "chore: release builds and publishes the hivemind CLI alongside hivemindd"
```

> **Note for the executor:** the tap/bucket repos are `causewayai/homebrew-causewayai` and `causewayai/scoop-causewayai`. Renaming `hivemindd.rb` → `hivemind.rb` there is a breaking change for anyone who ran `brew install hivemindd`. Confirm with the user whether to (a) rename, or (b) keep `hivemindd.rb` as the formula name and just add the second binary to its `install` block. Option (b) is lower-risk; default to it unless told otherwise.

---

### Task 22: README — CI log cache section + config table

**Files:**
- Modify: `README.md`

**Changes:**

1. Under `## Install`, note that the package now installs **two** binaries: `hivemindd` (daemon) and `hivemind` (client CLI).

2. New top-level section after `## Connecting a harness`:

```markdown
## CI log cache

`hivemind ci-logs` caches GitHub Actions run/job logs locally so a harness
that already fetched a run never re-hits the GitHub API for it.

    hivemind ci-logs run view <run-id> --log -R owner/repo

On a cache hit it prints the stored run summary and per-failed-job error
text. On a miss it runs the real `gh`, prints its output unchanged, saves
the raw log under `~/.hivemind/ci-logs/`, and records a summary entry plus
one entry per failed job (queryable via `memory_query` with
`external_id: "owner/repo#<run-id>"`).

The CLI starts `hivemindd` on demand — no `brew services` needed for this.

### Claude Code integration

    hivemind hook install

registers a `PreToolUse` hook that transparently rewrites
`gh run view … --log` commands to `hivemind ci-logs …`. Other harnesses:
see `docs/ci-log-cache-rules.md`.

### Managing the daemon

    hivemind daemon status
    hivemind daemon stop
```

3. Extend the config table with:

| Variable | Default | Description |
|---|---|---|
| `HIVEMIND_CI_LOG_DIR` | `~/.hivemind/ci-logs` | Raw CI log cache directory |
| `HIVEMIND_CI_LOG_MAX_AGE` | `720h` | Delete cached logs older than this |
| `HIVEMIND_CI_LOG_MAX_SIZE` | `524288000` | Soft cap (bytes) on total cache size |
| `HIVEMIND_CI_LOG_CLEANUP` | (unset) | Set to `off` to disable the hourly cleanup ticker |

**Commit:**

```bash
git add README.md
git commit -m "docs: README section for the CI log cache"
```

---

### Task 23: `memory_write` / `memory_query` tool doc updates in README

**Files:**
- Modify: `README.md` (`## Available tools` section, lines ~143+)

Document the new optional `memory_write` fields (`scope`, `source_type`, `external_id`) and the `memory_query` `external_id` field + the structured-only behavior when `query` is omitted. Keep the existing examples; add one showing an `external_id` upsert and one showing a structured lookup.

**Commit:**

```bash
git add README.md
git commit -m "docs: document memory_write/memory_query ETL fields"
```

---

### Task 24: DESIGN.md — append the resolved-questions log entry

**Files:**
- Modify: `docs/DESIGN.md`

Append a section (match the existing terse "what/why" style) covering:
- **`external_id` upsert path** — partial unique index `WHERE external_id IS NOT NULL`; `ON CONFLICT … DO NOTHING` + re-select; why insert-if-absent (finished-run logs are immutable).
- **structured-only `memory_query`** — skip embeddings when `query == ""`; same session-isolation rule as the semantic path.
- **Two binaries, one release** — `hivemind` (client) split from `hivemindd` (daemon); why not merged (leaves the verified daemon release path + `brew services` entrypoint untouched; keeps the client off the daemon's cgo/sqlite deps).
- **Daemon discovery via `daemon.port`** — supersedes the review's Unix-socket decision; a socket would have meant replacing the harness transport, and unauthenticated loopback TCP is already the Local Edition model. Cross-ref the design doc.
- **`PreToolUse` transparent rewrite is real** — earlier doubt (a stale public docs page said deny-only) was wrong; `../rtk/src/hooks/hook_cmd.rs` ships `hookSpecificOutput.updatedInput` in production with tests. `hivemind hook claude` copies that contract.
- **In-process hourly ticker** — first scheduled behavior in `hivemindd`; owned by `serve()`'s context, one sweep on startup, `HIVEMIND_CI_LOG_CLEANUP=off` for tests.

**Commit:**

```bash
git add docs/DESIGN.md
git commit -m "docs: record CI log ingestion design resolutions"
```

---

### Task 25: End-to-end manual verification

**Not automated.** Run through this and paste results into the PR.

**Setup:**
```bash
make build
export PATH="$PWD:$PATH"                 # so `hivemind` finds sibling `hivemindd`
export HIVEMIND_DATA_DIR=/tmp/hm-e2e/hivemind.db
rm -rf /tmp/hm-e2e
hivemind daemon status                   # -> "not running", exit 1
```

**Autostart + cache miss (pick a real failed run in a repo you can `gh` against):**
```bash
hivemind ci-logs run view <RUN_ID> --log -R causewayai/hivemind | head
# expect: daemon autostarts (see /tmp/hm-e2e/logs/daemon.log), gh runs,
#         the gh --log output is printed
cat /tmp/hm-e2e/daemon.port            # a port number
ls -R /tmp/hm-e2e/ci-logs/             # run.log present
```

**Cache hit (second run, no network):**
```bash
time hivemind ci-logs run view <RUN_ID> --log -R causewayai/hivemind
# expect: prints the cached summary + failure entries, noticeably faster,
#         no `gh` process (verify with `sudo fs_usage`/`strace -f` or by
#         temporarily renaming the `gh` binary)
```

**Structured query via the daemon:**
```bash
curl -s -X POST "http://127.0.0.1:$(cat /tmp/hm-e2e/daemon.port)/" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_query","arguments":{"session_id":"x","external_id":"causewayai/hivemind#<RUN_ID>","source":"github-actions"}}}'
# expect: results array with the summary + per-failed-job entries
```

**Hook:**
```bash
hivemind hook install --settings /tmp/hm-e2e/settings.json
echo '{"tool_name":"Bash","tool_input":{"command":"gh run view 123 --log -R o/r"}}' | hivemind hook claude
# expect: {"hookSpecificOutput":{...,"updatedInput":{"command":"hivemind ci-logs run view 123 --log -R o/r"}}}
```

**Retention:**
```bash
HIVEMIND_CI_LOG_MAX_AGE=1s hivemind daemon stop && hivemind daemon start
sleep 2
ls -R /tmp/hm-e2e/ci-logs/   # startup sweep should have emptied it
```

**Shutdown:**
```bash
hivemind daemon stop     # -> "stopped"; daemon.port and daemon.pid gone
```

**Commit:** none (verification only). Record results in the PR body.

---

## Execution note

Phases 1–2 are independent of 3–5 at the code level but 4 depends on 1–3 and 5 depends on 4. Execute in order. Run `make check` before every commit; keep every commit green.
