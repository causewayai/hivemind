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

**This phase is deliberately a proof of concept.** CI logs are a small,
well-understood use case chosen specifically to drive the `external_id`
ETL upsert path end-to-end and surface its design problems — upsert
semantics, structured-only query, direct `scope`/`source_type` on
`memory_write`, the first in-process background ticker — before a larger
source (Jira, source indexers) depends on that path. "Out of scope"
above therefore means "not in this POC," not "not planned": shaking out
those issues here, on low stakes, is part of the point.

**Non-goal carried over from the PRD:** hivemind does not become a
general-purpose document store. Raw log content lives as files on disk, not
as memory content in the SQLite store.

---

## Fetch location: harness-side, not hivemind-side

Hivemind gains no GitHub credentials, API knowledge, or polling logic. The
actual `gh`/GitHub API call happens outside hivemind's core — matching the
PRD's existing ETL-pipeline model where hivemind is source-agnostic and
simply stores what it's given. Hivemind never needs "GitHub Actions" to
exist as a concept anywhere in its core code; the CI-log shape lives
entirely in a documented tag/`external_id` vocabulary layered on top of the
general-purpose `memory_write` / `memory_query` primitives.

The concrete thing that makes the fetch-or-serve-from-cache decision is
the **`hivemind` client CLI** (see "Harness-side workflow") — the
general-purpose local client for the daemon, of which CI-log handling is
one subcommand. It runs on the harness's side of the boundary, shells out
to the user's already-authenticated `gh`, and talks to the local daemon
over the same interface any harness uses. It is a **separate binary from
`hivemindd`**, built from the same repository and shipped in the same
release — one Homebrew formula / Scoop manifest installs both — but the
GitHub-Actions-specific logic lives only in the CLI, and `hivemindd`
itself stays GitHub-unaware. The CLI starts the daemon on demand (see
"Daemon lifecycle"), so installing both is all the setup a user does.

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

**Raw log cache:** the ingesting side (the `hivemind` CLI below, or any
other ETL caller) saves the raw log text to
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

## Harness-side workflow: the `hivemind` CLI plus per-harness rewrite

The cache is only useful if harnesses actually consult it before hitting
the GitHub API, and hivemind cannot enforce that server-side — it only
sees the `memory_*` calls it is handed and cannot tell whether a live
fetch happened first. The adoption model instead mirrors the one
[`rtk`](https://github.com/rtk-ai/rtk) uses for the identical "route every
shell command through us" problem: a client binary, plus a transparent
command-rewrite hook for each harness that supports hooks, plus an
advisory rules snippet for those that don't.

### The `hivemind` CLI

The client CLI gains a `ci-logs` subcommand that mirrors `gh`'s argument
shape (see "Resolved decisions") and fronts the log-fetching `gh`
invocations. Given a request for a run's or job's logs it:

1. Queries the local hivemind daemon with
   `external_id = "<owner>/<repo>#<run_id>"` (or `#<job_id>`), no
   free-text query — the structured-only `memory_query` path.
2. **Cache hit:** prints the cached summary / failure entries (and, on
   request, the raw log file) to stdout in the shape `gh` would have
   produced, and exits 0. GitHub is never contacted.
3. **Cache miss:** execs the real `gh` (inheriting the user's existing
   `gh auth` state — no token in the CLI or in `hivemindd`), then writes
   the run-level summary and any failure entries via `memory_write`
   (`scope="user"`, `source_type="etl"`, `external_id` set) and saves the
   raw log under `~/.hivemind/ci-logs/...` before passing `gh`'s output
   through unchanged.

This subcommand is the only place the GitHub-Actions-specific ETL logic
lives. It is opt-in: nothing breaks if the CLI or its hook is absent, a
caller just gets a normal live `gh` every time.

### Routing harnesses through the CLI

- **Claude Code / Copilot (hook-capable):** a `PreToolUse` hook matches
  the log-fetching command forms (`gh run view --log`, `gh api
  .../logs`, `gh run download`, and the obvious `curl` to
  `api.github.com/.../actions/runs/.../logs`) and transparently rewrites
  them to `hivemind ci-logs ...` before execution. No model cooperation
  required — this is the rtk mechanism, and the reason it is stronger than
  a "block and tell the model to call `memory_query` instead" nudge, which
  the model can rephrase around.
- **Cursor / Windsurf / Cline / rules-only harnesses:** an advisory
  snippet ("use `hivemind ci-logs` instead of `gh ... --log` for CI log
  fetches") dropped into the harness's rules file — same posture as rtk's
  `.windsurfrules` / `.clinerules`: best-effort, not enforced.
- **Anything else, or a human at a terminal:** call `hivemind ci-logs`
  directly, or wrap `gh` in a shell function that routes log fetches
  through it.

### What this does and does not guarantee

- Inside a harness with the rewrite hook installed, cache-first behavior
  is effectively enforced — transparently, without the model's
  cooperation.
- It is still per-harness integration work: a new harness gets nothing
  until someone writes its adapter.
- It is opt-out-able and uninstallable — a well-engineered default, not a
  hard invariant. Acceptable because a cache miss costs only a redundant
  fetch, never wrong data.
- Regex-matching command forms in a hook only catches anticipated
  invocations; a raw HTTP client inside a script still slips past. The
  rules snippet and direct-invocation paths are the fallback there.

Every step above is expressed in general-purpose hivemind primitives:
`hivemindd` stores and returns entries keyed by `external_id`/tags and
does not know the data came from GitHub Actions. The "GitHub Actions"
shape lives entirely in the tag/`external_id` vocabulary the CLI and the
docs define.

---

## Daemon lifecycle: the CLI autostarts the daemon

`hivemind ci-logs` (and any other daemon-backed subcommand) needs
`hivemindd` running. Making the user `brew services start hivemindd` first
is exactly the friction this CLI is meant to remove — and that command
doesn't exist at all on the tarball/Scoop install path.

**Baseline: lazy autostart.** On a command that needs the daemon, the CLI:

1. Tries to connect to the daemon at its well-known local address.
2. On connection refused, takes an exclusive lock
   (`~/.hivemind/daemon.lock`), re-checks, and if still down execs
   `hivemindd` detached — preferring a `hivemindd` sibling of the running
   `hivemind` binary, falling back to `PATH`.
3. Poll-connects with backoff up to a short timeout, then proceeds — or
   fails with the daemon's captured stderr if it never came up.

The lock makes concurrent CLI invocations cooperate instead of racing two
daemons into existence. This path is fully cross-platform and needs zero
user setup.

**OS service management stays layered on top, not replaced.** A user who
wants a persistent daemon (survives logout/reboot, restarts on crash)
runs `brew services start hivemindd` / a systemd user unit as today; the
CLI detects the already-running daemon and just connects. Autostart and
service management don't conflict — first to bind the address wins, the
other no-ops. An autostarted daemon stays resident (it has the hourly
cleanup ticker to run regardless of client activity); `hivemind daemon
stop` shuts it down.

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

## Resolved decisions for the implementation plan

These were open during design review and are now settled. The plan should
encode them as-is; only genuinely sub-implementation choices (exact SQL,
struct layout) are left to the plan author's discretion.

- **Retention thresholds:** default 30-day age cutoff and 500 MB soft
  size cap on the raw-log cache (whichever trips first), overridable via
  `HIVEMIND_CI_LOG_MAX_AGE` (duration) and `HIVEMIND_CI_LOG_MAX_SIZE`
  (size), following the existing `HIVEMIND_*` convention.
- **Unique index + upsert:** a *partial* unique index on
  `(source, external_id, scope)` with `WHERE external_id IS NOT NULL`, so
  existing NULL-`external_id` rows are unaffected. Upsert via
  `INSERT ... ON CONFLICT(source, external_id, scope) DO NOTHING`
  followed by a `SELECT` for the id.
- **Cleanup ticker:** disable-able with `HIVEMIND_CI_LOG_CLEANUP=off`
  (tests set this). Runs as a goroutine owned by `run()`, driven by a
  `time.Ticker`, cancelled through the existing shutdown context. Runs
  one sweep immediately on startup rather than waiting a full hour.
- **`log_path`:** a `log_path:<path>` tag, not a dedicated `MemoryEntry`
  field. No schema change beyond the unique index this phase.
- **CLI grammar:** the `hivemind` CLI mirrors `gh`'s argument shape as
  closely as practical (so the rewrite hook is near-literal substitution
  and humans can alias it), chosen deliberately to leave room for growth.
  Ships `ci-logs` only this phase; plain `memory_*` verbs come later when
  something needs them. Reuses the daemon's existing MCP transport — no
  new endpoint.
- **Rewrite-hook match set:** match only `gh run view` *with* `--log` or
  `--log-failed`; `gh api` paths ending `/logs`; `gh run download`; and
  `curl` / `gh api` to `.../actions/runs/<id>/logs` or
  `.../jobs/<id>/logs`. Anything with `--json` and no `--log` passes
  straight through. Deliberately under-match: a missed rewrite is a live
  fetch, a wrong rewrite breaks a command.
- **POC harness coverage:** Claude Code native `PreToolUse` hook, plus one
  generic copy-paste rules snippet for everything else. First-class
  adapters for other harnesses tracked in
  [#6](https://github.com/causewayai/hivemind/issues/6).
- **Hook distribution:** ships in this repo; installed by an idempotent
  `hivemind hook install` (with `--print` to emit the JSON for manual /
  other use); documented copy-paste is the fallback.
- **Daemon local address:** the daemon keeps its existing loopback TCP
  HTTP listener (harnesses already connect to it, and the port can vary
  via `HIVEMIND_PORT`) and, on startup, writes the port it actually bound
  to `~/.hivemind/daemon.port` (plus its PID to `~/.hivemind/daemon.pid`).
  The `hivemind` CLI reads that file and dials the same
  `127.0.0.1:<port>` MCP endpoint, behind a single "dial the daemon"
  helper. **This supersedes the review's Unix-domain-socket choice:** a
  socket would have meant replacing or duplicating the transport
  harnesses already use — out of scope for this POC — and its "filesystem
  perms as access control" benefit is moot while unauthenticated loopback
  TCP is already the accepted Local Edition model. A socket can be
  revisited later without touching the CLI (the dial helper is the only
  thing that would move).
- **Spawn-race:** both guards — the CLI takes a `flock` on
  `~/.hivemind/daemon.lock` before spawning, and `hivemindd` binds its
  address with fail-if-exists semantics as a backstop so a stray second
  daemon exits immediately and the CLI's retry-connect wins.
- **Version skew:** the CLI sends its version on connect. Same version →
  proceed silently. Daemon older than the CLI → refuse the current
  operation, restart the daemon (via the service manager if it is
  service-managed, otherwise stop-and-respawn), then retry.
- **Daemon logs:** a non-service-managed daemon writes to
  `~/.hivemind/logs/daemon.log` with simple size-based rotation; a
  service-managed one inherits the service manager's stdout/stderr
  handling as today.
- **Idle-shutdown:** *not* in this phase — an autostarted daemon stays
  resident (the cleanup ticker needs it) and exits only on
  `hivemind daemon stop` or a signal. Whether to add idle-shutdown later
  is tracked in [#7](https://github.com/causewayai/hivemind/issues/7).
