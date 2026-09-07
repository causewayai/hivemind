# Hivemind — Design Notes

Open design questions and decisions that follow from the PRD but aren't requirements themselves.

---

## Tenancy in the Structured Store

The PRD's `scope` field (session/user/team/sub-team) governs visibility *within* an account — it does not identify which tenant/organization a row belongs to. The Cloud Edition's PostgreSQL store needs a separate notion of tenancy to isolate one organization's data from another's.

**Candidate approach:** add a `tenant_id` (root/top-level team) column to every table — users, teams, memory entries, permissions — and enforce isolation with Postgres row-level security (RLS) policies keyed on it, kept distinct from `scope`.

**Tradeoff to resolve:** shared schema + RLS (cheap, scales to many small teams, but isolation correctness depends on every policy being right — a missed policy leaks across tenants) vs. schema-per-tenant or database-per-tenant (isolation enforced at the infrastructure level, but heavier operationally — migrations and connection management multiply per tenant).

---

## local-daemon-static-binary

The PRD's Local Edition says "distributed as a single self-contained binary." The local daemon implementation (`docs/plans/2026-09-04-local-daemon.md`) uses `mattn/go-sqlite3` and `sqlite-vec-go-bindings/cgo`, both of which compile C code into the binary via cgo. This produces one file with no separate `.so`/`.dylib` to ship, but it still dynamically links the host's libc — it is not a fully static, drop-anywhere binary (a Linux build isn't guaranteed to run against an arbitrary/older glibc, for instance).

**Candidate fix, if true portability is required:** switch to the WASM-based `sqlite-vec` bindings paired with `ncruces/go-sqlite3` (a cgo-free SQLite driver), producing a genuinely static binary at the cost of routing SQLite calls through a WASM runtime. Deferred as a follow-up — not resolved in the local daemon plan.

---

## Hybrid retrieval needs a relevance cutoff, not just re-ranking

Task 8 of `docs/plans/2026-09-04-local-daemon.md` originally implemented hybrid retrieval as: pull a wide vector-KNN candidate pool (`TopK * 5`), then post-filter that pool by tags/scope/source. During TDD execution this failed its own test (`TestQuery_HybridSemanticAndTagFilter`): an entry that matched the tag filter but was semantically unrelated to the query (`far` embedding) still appeared in results, because plain top-K ranking has no way to exclude a candidate just for being distant — it only bounds *how many* results come back, not *how relevant* they are.

**Fix applied:** `Store.Query` now discards vector candidates whose L2 distance exceeds `defaultMaxDistance` (currently `1.0`, an unexported constant in `internal/store/memory.go`) before structured filters run. This makes a tag match insufficient on its own — the candidate must also be semantically close.

**Open item:** `1.0` is tuned against `HashProvider`'s output spread ([-1,1) per dimension), not any real embedding model's distance scale. When a real provider (OpenAI-compatible endpoint, etc. — see plan's "After this plan" section) is plugged in, this threshold will need recalibration, and likely should become configurable rather than a hardcoded constant.

**Downstream consequence for test fixtures (discovered in Task 10):** because `HashProvider` is explicitly non-semantic, two unrelated strings hash to essentially uncorrelated vectors — so a fixture that writes content like `"A's secret"` and later queries `"secret"`, expecting the write to come back, will usually fail the distance cutoff purely by hash-noise coincidence, with no bearing on the behavior actually under test (session/scope isolation, not semantic recall). The fix used in `internal/mcpserver/query_test.go` is to pin fixtures to an explicit precomputed embedding (via `MemoryWriteInput.Embedding` / `CreateMemoryInput.Embedding`) matching the query's embedding, so semantic distance is trivially satisfied and the test isolates the structural behavior it's named for. Any later test exercising `memory_query`/`Store.Query` for non-semantic purposes should follow the same pattern.

---

## MCP SDK v1.7.0 tool-handler shape (resolved during Task 12)

The plan (`docs/plans/2026-09-04-local-daemon.md`, Task 12) assumed `mcp.AddTool` handlers had the shape `func(ctx, In) (*Out, error)`, flagged as the area of "least certainty" in the plan's own research. Reading the installed `github.com/modelcontextprotocol/go-sdk@v1.7.0` source directly (`mcp/tool.go`) showed the actual generic type is:

```go
type ToolHandlerFor[In, Out any] func(_ context.Context, request *CallToolRequest, input In) (result *CallToolResult, output Out, _ error)
```

— three parameters (ctx, request, input) and three return values (result, output, error), not two and two. Returning `(nil, out, nil)` is sufficient; the SDK auto-marshals a non-nil `out` into `CallToolResult.StructuredContent` when `result` is nil (see `toolForErr` in `mcp/server.go`).

**Resolution:** kept the existing unit-tested `handle*` methods (`handleMemoryWrite`, `handleMemoryQuery`, `handleListScopes` in `internal/mcpserver`) with their original simple `(ctx, In) -> (*Out, error)` shape — their tests didn't need to change. Added a thin adapter layer, `internal/mcpserver/adapters.go` (`HandleMemoryWrite`, `HandleMemoryQuery`, `HandleListScopes`), matching `ToolHandlerFor` exactly, used only when wiring `mcp.AddTool` in `cmd/hivemindd/main.go`. (`handleListScopes` later dropped its `error` return during lint cleanup — see below — since `unparam` correctly noted it could never fail; the adapter now supplies `nil` for the error itself.)

---

## Linting, architecture checks, and naming consistency (golangci-lint)

Set up before the first push, per explicit request. Config lives at `.golangci.yml` (v2 schema); run via `make lint` or `make check` (lint + test). Requires `golangci-lint` v2 (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`).

**Linters enabled beyond the v2 `standard` set** (`errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`): `revive` (naming/style, incl. exported-symbol doc comments), `gocritic` (bug-pattern diagnostics), `misspell`, `unconvert`, `errname`, `unparam` (catches parameters/returns that are always the same value — this is what caught `handleListScopes`'s dead `error` return), `copyloopvar`, `sqlclosecheck` and `rowserrcheck` (both `database/sql`-specific, relevant since `internal/store` is all raw SQL), plus `gofmt`/`goimports` as formatters.

A first run against the Task 1–13 implementation surfaced 42 real issues, all fixed rather than suppressed: unchecked `Close`/`Rollback` errors (wrapped in `defer func() { _ = x.Close() }()`), a `log.Fatal` after an active `defer` that would never run (`main` restructured into `main`/`run() error`), an `append` result assigned to the wrong variable, missing package and exported-symbol doc comments throughout, and dead `ctx` parameters — the last of which led to threading `context.Context` through `embedding.Provider.Embed` (a real design improvement: a future HTTP-backed provider will need it for cancellation) rather than just underscoring the parameter.

**Architecture enforcement via `depguard`:** rules encode this project's layering — `internal/config`, `internal/embedding`, and `internal/store` are foundational and may not import any other package in this module; `internal/mcpserver` may depend on those but not on `cmd/hivemindd` (the composition root). Verified empirically by temporarily injecting a real violation for each of the four rules and confirming `golangci-lint` actually flags it (not just "no rules matched anything") — see git history for `.golangci.yml` around the date of this note if the verification steps are needed again.

**Non-obvious gotcha, worth preserving:** this depguard version's `files` glob matching requires **both** a leading `**/` and a trailing `**` — i.e. exactly `"**/internal/config/**"`. Any of the "obvious" alternatives silently match nothing (no error, just 0 issues, indistinguishable from "no violations"):
- `"internal/config/**/*.go"` — a bare `dir/**/*.go` requires at least one intervening subdirectory in this doublestar implementation, so it never matches files directly in `dir`.
- `"internal/config/**"` (no leading `**/`) — also silently matches nothing; the leading `**/` is required to skip whatever base path prefix the matcher compares against.

Because a wrong glob fails *silently* (the linter reports 0 issues either way), any new `depguard` rule added later should be verified the same way this one was: inject a real cross-layer import, confirm it's flagged, then revert.

---

## Windows CI cgo toolchain

The CI/CD plan (`docs/plans/2026-09-05-cicd-distribution.md`, Task 2) flagged the Windows leg as its least-proven step, since both `mattn/go-sqlite3` and `sqlite-vec-go-bindings` compile C code via cgo. Two distinct, unrelated failures had to be resolved in sequence before it went green — worth recording so neither gets "fixed" back into existence by someone trying to simplify the workflow later.

**Failure 1 — `sqlite3ext.h: No such file or directory`.** `sqlite-vec-go-bindings`'s C shim `#include "sqlite3ext.h"` without shipping that header itself; it relies on it being reachable on the C include path. That header happens to already be discoverable on the Linux/macOS GitHub-hosted runner images (likely a system `sqlite3-dev` package), but not on `windows-latest`. `mattn/go-sqlite3` vendors the header at its own module root, so the fix points `CGO_CFLAGS` at that module's directory: `export CGO_CFLAGS := -I$(subst \,/,$(shell go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3))` in the `Makefile`. Two details made this non-obvious:
- `go list -m -f '{{.Dir}}'` needs the module already present in the local module cache to report a real path; on a cold Windows runner it wasn't yet (the module only got fetched later, during `go vet`'s own dependency resolution), so an explicit `go mod download` step was added to the workflow *before* `make check` runs, to guarantee the module is on disk by the time `Makefile`'s top-level `:=` assignment evaluates the `$(shell ...)` call.
- `go list -m` on Windows returns a backslash-separated path. Substituted unquoted into a Bash-run recipe command line, an unescaped backslash is consumed as a shell escape character, silently collapsing the path (`C:\Users\...` → `C:Users...`) and reproducing the exact same "header not found" error even though `CGO_CFLAGS` looked superficially correct. `$(subst \,/,...)` normalizes to forward slashes inside Make itself (not the recipe shell) before the path ever reaches Bash — MinGW GCC accepts forward-slash paths on Windows too, so this is a no-op on macOS/Linux.

**Failure 2 — gofmt flags every file as unformatted, once cgo actually started compiling.** `windows-latest`'s Git checkout was converting files to CRLF on checkout, and `gofmt` (run via `golangci-lint`) treats CRLF as "not properly formatted" regardless of the content's actual formatting. Fixed with a repo-root `.gitattributes` (`* text=auto eol=lf`), which normalizes line endings to LF on checkout on every platform — not just a CI workaround, since a contributor's own Windows git config (`core.autocrlf=true` is a common default) would otherwise hit the same thing locally.

With both fixed, `windows-latest` needed no MSYS2/mingw-w64 fallback — the Strawberry Perl-bundled GCC that ships on the GitHub-hosted Windows runner image (added to `PATH` via the `Configure MinGW GCC` step) was sufficient once the include path and line-ending issues were resolved.

---

## `zip` isn't installed on windows-latest

`.github/workflows/release.yml`'s Windows leg archives the built binary with `zip` for consistency with the `.tar.gz` archiving used on macOS/Linux. That command doesn't exist on the GitHub-hosted `windows-latest` runner image — it's not preinstalled the way it is on the Ubuntu/macOS images — and failed with `zip: command not found` (exit 127) the first time a real release tag was pushed, since this failure mode only shows up on `push: tags`, not on the regular `pull_request`/`push: main` CI workflow, which never runs `release.yml` at all.

**Fix:** use PowerShell's built-in `Compress-Archive` cmdlet instead (`shell: pwsh`, available by default on every Windows runner, no install needed) rather than trying to install `zip` via Chocolatey. Produces an equivalent `.zip` for the Scoop manifest.

---

## `publish` job's GITHUB_TOKEN needs explicit `contents: write`

The next issue hit on the same real-tag-push test (after the `zip` fix above): `gh release create` failed with `HTTP 403: Resource not accessible by integration`. The default per-run `GITHUB_TOKEN` only carries `contents: read` (a repo/org security default, restricting the automatic token to the minimum unless a workflow opts into more), so creating a release — a write operation — was rejected.

**Fix:** add an explicit `permissions: contents: write` block scoped to just the `publish` job (not the whole workflow, since `build` doesn't need elevated permissions — it only checks out the repo). This is unrelated to the GitHub App token used for the Homebrew/Scoop pushes — that's a separate, narrower-scoped credential for two external repos; this permission only affects the default token's access to `causewayai/hivemind` itself.

---

## CI log ingestion — resolved design questions

`docs/plans/2026-09-06-ci-log-ingestion.md` (executed 2026-09-06/07) added a local cache of GitHub Actions run/job logs so a harness that already fetched a run never re-hits the GitHub API. It is the first real user of the `external_id` ETL path the PRD describes. Decisions worth recording:

**`external_id` upsert is insert-if-absent, not update-in-place.** Logs for a finished CI run are immutable, so `memory_write` with an `external_id` set does `INSERT ... ON CONFLICT(source, external_id, scope) WHERE external_id IS NOT NULL DO NOTHING` and, on a conflict, re-selects and returns the existing entry. The `WHERE external_id IS NOT NULL` predicate is load-bearing twice over: it makes the unique index *partial* so index creation on an upgraded binary can't fail on the pile of existing NULL-`external_id` rows, and it must be repeated verbatim in the `ON CONFLICT` target or SQLite rejects the statement. `CreateMemory` never returns `(nil, nil)` on the conflict path — if the row vanishes between the failed insert and the re-select (a cleanup sweep racing a re-ingest) it returns an explicit error instead, so callers can safely dereference the result.

**`memory_query` gained a structured-only path.** When `query` is empty, the handler skips embedding generation and the semantic-distance cutoff entirely and returns `ListMemories` results (exact `external_id`/`tags`/`source` match, newest first). Session isolation holds identically on both paths — two separate lookups, own session scope plus all user scope, never another session's. Before this, `memory_query` always embedded `in.Query` (even `""`) and applied `defaultMaxDistance`, which is nonsense for an exact "is run X cached?" lookup.

**`memory_write`'s `session_id` is schema-required, so ETL callers pass a dummy.** The go-sdk marks any struct field without `,omitempty` as JSON-schema-`required`; `MemoryWriteInput.SessionID` has no `,omitempty`, and that flag is currently the *only* enforcement that a session-scope write carries a session id (the handler has no independent check). Rather than weaken that, the `hivemind` CLI passes `session_id: "hivemind-cli"` on its `scope=user` writes; the handler blanks it for user scope, so it's harmless. Making `session_id` genuinely optional (schema change **plus** a handler-side check for `scope=session`) is deferred to its own task.

**Two binaries, one repo, one release.** `hivemindd` (daemon) and `hivemind` (client CLI) are separate `cmd/` binaries built and shipped together — the Homebrew formula and Scoop manifest were renamed `hivemindd` → `hivemind` and now install both, keeping the `hivemindd` launchd/systemd service block. They stay separate rather than merging so the client links neither the store nor its sqlite-vec cgo: the shared vocabulary/parsing package `internal/cilog` is kept pure (stdlib only), and the store-dependent retention sweep + hourly ticker live in `internal/cilog/retention`, imported only by `hivemindd`. `go list -deps ./cmd/hivemind` shows no `internal/store` — the CLI is cgo-free and cross-compiles without a C toolchain. (It is still ~12 MB: that's the MCP client SDK, not cgo.)

**Daemon discovery is a port file, not a Unix socket.** The design review picked a Unix domain socket for the CLI↔daemon channel; execution superseded that. `hivemindd` keeps its existing loopback-TCP HTTP listener (harnesses already point MCP clients at it, and the port can vary via `HIVEMIND_PORT`) and, on startup, writes the port it actually bound to `~/.hivemind/daemon.port` (plus its pid to `daemon.pid`), removing both on graceful shutdown. The `hivemind` CLI reads that file and dials the same endpoint. A socket would have meant duplicating or replacing the transport harnesses already use, and its "filesystem perms as access control" benefit is moot while unauthenticated loopback TCP is already the accepted Local Edition model. `run()` was refactored into a context-cancelable `serve()` that also handles SIGINT/SIGTERM and hosts the cleanup ticker goroutine.

**The CLI lazily autostarts the daemon.** `hivemind ci-logs` (and `hivemind daemon start`) call `ensureDaemon`: dial the port file; on failure take an exclusive `flock` on `~/.hivemind/daemon.lock` (an `O_CREATE|O_EXCL` spin with 30s stale-steal on Windows, which has no advisory locking), re-dial in case a sibling won the race, then `exec` a detached `hivemindd` (resolved as `$HIVEMIND_DAEMON_BIN`, else a sibling of the CLI binary, else `PATH`) and poll-connect for up to 5 s. An autostarted daemon stays resident — there is no idle-shutdown in this phase (tracked as causewayai/hivemind#7).

**`PreToolUse` transparent rewrite is real.** An earlier read of a stale public docs page suggested Claude Code's `PreToolUse` hook could only allow/deny, not rewrite a command. Checking `../rtk/src/hooks/hook_cmd.rs` disproved that: it ships `hookSpecificOutput.updatedInput` in production with tests. `hivemind hook claude` copies that contract — it reads the PreToolUse payload on stdin and, when `tool_input.command` is a bare `gh run view … --log[-failed] …` (leading command, no shell metacharacters), emits an `updatedInput` response rewriting it to `hivemind ci-logs run view …`; anything else produces no output and exit 0. The matcher deliberately under-matches — a missed rewrite costs a redundant live fetch, a wrong rewrite breaks a command — so compound commands, redirects, substitutions, and non-leading `gh` are all left alone. `hivemind hook install` merges the hook entry into `~/.claude/settings.json` idempotently; other harnesses get a copy-paste rules snippet (`docs/ci-log-cache-rules.md`).

**Retention is a soft cap enforced by an in-process hourly ticker.** `hivemindd` now runs its first scheduled behavior: a goroutine owned by `serve()`'s context that sweeps the CI-log cache on startup and every hour, deleting `*.log` files past `HIVEMIND_CI_LOG_MAX_AGE` (default 720h) and then, if the remainder still exceeds `HIVEMIND_CI_LOG_MAX_SIZE` (default 500 MiB), the oldest until it doesn't — and deleting each doomed file's backing memory entries (found via their `log_path:` tag) so an entry never outlives its log. Non-positive limits disable their pass rather than purging everything. `HIVEMIND_CI_LOG_CLEANUP=off` disables the ticker (tests use this). Because `go-sqlite3` runs with foreign-key enforcement off, `DeleteMemory` deletes `memory_tags` rows explicitly rather than relying on the schema's `ON DELETE CASCADE`.
