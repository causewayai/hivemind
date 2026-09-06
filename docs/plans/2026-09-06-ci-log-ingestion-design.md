# CI Log Ingestion — Requirements

Requirements for caching GitHub Actions CI run/job logs locally in hivemind,
so a harness that has already fetched a run's logs doesn't need to call the
GitHub API again for it — this session or a future one.

---

## Motivation

During CI/CD work, a harness (e.g. Claude Code) routinely calls
`gh run view --log` / `gh api .../logs` repeatedly for the same run while
debugging a failure, and again in later sessions when related work resumes.
Each call is a live GitHub API round-trip with no local memory of what was
already fetched. Hivemind's PRD already envisions exactly this shape of
problem — "ETL pipeline writes... e.g., Jira, GitHub..." — but that path has
never been implemented: `memory_write` today only supports harness-session
writes, the `external_id` column exists in schema but is never populated,
and there's no upsert or unique-index support for it. This phase is the
first real driver for building that capability, with CI logs as the
concrete use case.

## Scope

**In scope:** GitHub Actions workflow run/job logs specifically.

**Out of scope (this phase):** generalizing to arbitrary command-output
caching, other CI providers, or other ETL sources (Jira, source indexers,
etc.) — those are natural future extensions of the same mechanism, not
needed now.

**Non-goal carried over from the PRD:** hivemind does not become a
general-purpose document store. Raw log content lives as files on disk, not
as memory content in the SQLite store.

---

## Fetch location: harness-side, not hivemind-side

Hivemind does not gain GitHub credentials, API knowledge, or polling logic.
The harness (or any external ETL process) does the actual `gh`/GitHub API
call and writes the result into hivemind — matching the PRD's existing
ETL-pipeline model where hivemind is source-agnostic and simply stores what
it's given. This also means hivemind never needs GitHub Actions to exist as
a "hivemind concept" anywhere in its core code — it's purely a convention
built on top of general-purpose primitives (`memory_write`/`memory_query`
plus a documented tag/external_id vocabulary).

---

## Data model

**Two kinds of entries per ingested run:**

- **One run-level summary** — status (success/failure/cancelled), workflow
  name, commit SHA, branch/PR number, trigger event, duration, and which
  jobs failed if any.
- **One entry per failed job/step** — the specific error text (e.g. the
  actual `"zip: command not found"` line), not the whole log, so semantic
  search over failures stays precise. Successful jobs get no dedicated
  entry — the run-level summary already says "all green," and there's no
  unique signal worth indexing separately.

**Scope & source:**
- `scope = "user"` — written directly by the ETL write path (see below),
  not session-scoped. This is what makes the cache persist and be shared
  across sessions/harnesses on the machine, per the PRD's statement that
  (unlike harness writes) ETL writes can target any scope the pipeline's
  credentials permit.
- `source_type = "etl"`, `source = "github-actions"` (or similar).

**`external_id` convention** (must be unique per `(source, external_id,
scope)`, and stable across repeated ingestion attempts so re-ingestion is
idempotent):
- Run-level summary: `"<owner>/<repo>#<run_id>"`
- Job-level failure entry: `"<owner>/<repo>#<run_id>#<job_id>"`

**Tags convention** (flat `key:value` strings, matching the existing tag
model — these are what structured lookup filters on):
`repo:<owner>/<repo>`, `run_id:<id>`, `job:<name>`, `workflow:<name>`,
`status:<pass|fail>`, `commit:<sha>`.

**Raw log cache:** the harness saves the raw log text to
`~/.hivemind/ci-logs/<owner>/<repo>/<run_id>/<job_id>.log` *before* writing
the summary/failure entry. The entry records that path (e.g. a `log_path:`
tag or a dedicated field) so a harness needing more detail than the summary
captured can read the file directly — no re-fetch needed.

---

## hivemind changes: write and read paths

### `memory_write` extension

Add optional fields to `MemoryWriteInput`, currently hardcoded to
`scope="session"` / `source_type="harness"`:

- `scope` (optional, defaults to `"session"` — preserves today's behavior
  for existing callers)
- `source_type` (optional, defaults to `"harness"`)
- `external_id` (optional)

When `external_id` is set, the write is an **upsert keyed on
`(source, external_id, scope)`**: if a matching entry already exists, skip
the write (CI logs for a completed run are immutable — no need for
update-in-place semantics, just idempotent insert-if-absent). This requires
a new unique index on `(source, external_id, scope)` in
`internal/store/schema.go` — the PRD calls for this composite key but it
was never added.

### `memory_query` extension

Add an optional `external_id` filter param to `MemoryQueryInput`. When the
caller supplies `external_id` and/or `tags`/`source` but **no free-text
`query`**, skip the embedding + semantic-distance-cutoff path entirely and
return structured matches directly (e.g. ordered by `created_at desc`).

This fixes a real gap: today's `memory_query` always embeds `in.Query` and
applies `defaultMaxDistance` filtering even when the caller wants an exact
lookup ("do I already have run X cached?"), which doesn't fit a fuzzy
semantic model at all. This fix is generally useful beyond CI logs — any
future exact/structured lookup need benefits from it.

### Security tradeoff (accepted, but explicit)

Allowing `memory_write` to accept `scope`/`source_type` directly means any
MCP caller can claim `scope="user"`/`source_type="etl"`, narrowing the
PRD's stated invariant that harnesses can't self-promote memory scope.
This is a smaller step for Local Edition specifically than it would be for
Cloud Edition — Local Edition already has no authentication and implicitly
shares memory across all harnesses on the machine per the PRD — but it's a
real narrowing of that boundary and should be documented as a deliberate
tradeoff, not left implicit.

---

## Harness-side workflow

Before fetching CI logs via `gh`/the GitHub API, a harness should:

1. Call `memory_query` with `external_id = "<owner>/<repo>#<run_id>"` (or
   `#<job_id>` for a specific job), no free-text query.
2. **If found:** use the cached summary/failure entries directly; read the
   raw log file only if more detail is needed than the summary captured.
3. **If not found:** fetch from GitHub as normal, then write the run-level
   summary and any failure entries via `memory_write`
   (`scope="user"`, `source_type="etl"`, `external_id` set), and save the
   raw log to the on-disk cache path.

This is a **documented convention for harnesses to follow** (added to
hivemind's README/docs), not new server-enforced behavior — hivemind
doesn't know or care that the data came from GitHub Actions specifically.

---

## Retention policy

- **Soft max, not a hard limit:** define a target cap (e.g. total raw-log
  cache size, and/or max age) that can be temporarily exceeded between
  cleanup passes. Writes are never blocked or rejected for being over the
  cap.
- **Built-in hourly cleanup ticker inside `hivemindd`:** the daemon runs a
  background job every hour that deletes raw log files — and their
  corresponding summary/failure memory entries, so a memory entry never
  outlives its backing file — past the configured age/size threshold. This
  is a new precedent for hivemind: it has had no background/scheduled
  behavior of any kind until now, only work done in response to an MCP
  call.
- In-process and periodic rather than externally triggered, so no OS-level
  cron/launchd/Task Scheduler entry is needed for this — one less moving
  part for install/setup.
- Exact thresholds (age/size) are a tuning detail for the implementation
  plan, not locked in at the requirements stage.

---

## Open items for the implementation plan

- Exact retention thresholds (age cutoff, size cap) and how they're
  configured (env var, following the existing `HIVEMIND_*` convention).
- Exact shape of the new unique index and how upsert-on-conflict is
  implemented in SQLite (`INSERT ... ON CONFLICT DO NOTHING` vs.
  check-then-insert in a transaction).
- Whether the hourly ticker needs to be pausable/disableable (e.g. for
  tests), and how it's wired into `cmd/hivemindd/main.go`'s startup/shutdown
  without blocking `run()`.
- Whether `log_path` should be a dedicated `MemoryEntry` field or just a
  convention-tagged value — affects schema vs. purely additive change.
