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
