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
