# Session Handoff — 2026-09-06

Written to resume work in a future session. Covers what was just finished
(CI/CD + Homebrew/Scoop distribution) and what's queued up next (CI log
ingestion). Read this first, then follow the links.

---

## What's done: CI/CD + distribution pipeline

All 8 tasks in `docs/plans/2026-09-05-cicd-distribution.md` are complete
and merged to `main`. Full rationale and every gotcha hit along the way is
in `docs/DESIGN.md` (search for section headers — each fix has its own
"what broke, why, what fixed it" entry) and
`docs/plans/2026-09-05-cicd-distribution-design.md` (the original design
doc, kept up to date through execution).

**Working today:**
- `.github/workflows/ci.yml` — 4-platform matrix (macOS arm64/amd64, Linux
  amd64, Windows amd64) on every PR/push to `main`. All green.
- `.github/workflows/release.yml` — tag-triggered (`v*.*.*`) build +
  publish. Builds all 4 platforms, creates a GitHub Release, pushes an
  updated Homebrew formula to `causewayai/homebrew-causewayai` and a Scoop
  manifest to `causewayai/scoop-causewayai`, authenticating via the
  `causeway-release-bot` GitHub App (secrets `RELEASE_APP_ID` /
  `RELEASE_APP_PRIVATE_KEY` on `causewayai/hivemind`).
- `hivemindd --version` reports ldflags-injected version/commit/date.
- **End-to-end verified for real**, not just in CI: `v0.0.3-test` tag →
  full pipeline → `brew tap causewayai/causewayai` → `brew install
  hivemindd` → `brew services start hivemindd` → live daemon answering an
  MCP `initialize` call. Actually ran this, not simulated.

**Repo visibility (changed mid-session — important context):**
- `causewayai/hivemind` is now **public**. Release assets need no auth to
  download.
- `causewayai/homebrew-causewayai` and `causewayai/scoop-causewayai` (the
  tap/bucket) are **still private**. Per the user: "further changes to
  come soon" to make those public too. Until then, end users need
  `HOMEBREW_GITHUB_API_TOKEN` (Homebrew) or `scoop config gh_token`
  (Scoop) — see the README's Install section for exact commands.
- If/when the tap/bucket repos go public, `docs/DESIGN.md`'s "Homebrew
  can't download release assets from a private repo without extra help"
  section documents a fix (asset-ID + Authorization header) that turned
  out to be unnecessary once `hivemind` itself went public — it's *not*
  currently wired into `release.yml` (PR #4 implementing it was closed as
  moot). If `hivemind` ever goes private again, or a similar
  private-repo-download situation comes up elsewhere, that write-up has
  the working fix, verified with a real `brew install` at the time.

**Loose ends nobody's asked to resolve yet, flagging so they don't get
forgotten:**
- Several now-merged feature branches still exist, both locally and on
  `origin`: `ci/add-workflow`, `cicd-distribution`, `fix/release-token-permissions`,
  `fix/windows-zip-archive`. Offered to clean these up; not yet actioned.
- `fix/homebrew-private-repo-download` branch/PR (#4, closed not merged)
  still exists with the asset-ID-auth fix on it, in case it's needed again.
- `docs/install-instructions` branch also still exists (its PR #5 is
  merged; branch just hasn't been deleted).
- Test releases `v0.0.1-test`, `v0.0.2-test`, `v0.0.3-test` (tags +
  GitHub Releases) are intentionally left in place per the user — not
  cleaned up.
- An untracked `scripts/set-release-app-secrets.sh` sits locally in the
  user's own `main` checkout working directory (not in git — it was
  deliberately squashed out of history per the user's request, since
  they plan to put infra scripts like this in a separate future infra
  repo instead). Harmless, but flagged in case it's confusing later.

---

## What's next: CI log ingestion

Requirements captured (brainstorming phase complete, not yet planned or
implemented): `docs/plans/2026-09-06-ci-log-ingestion-design.md`.

**One-sentence pitch:** cache GitHub Actions run/job logs locally in
hivemind (summary + failure entries in the store, raw logs as files on
disk) so a harness that already fetched a run's logs — this session or a
past one — doesn't need to hit the GitHub API again for it.

**Why this matters beyond the immediate use case:** this is the first real
driver for hivemind's `external_id`-based ETL upsert path, which the PRD
describes but which has never been implemented — `memory_write` today only
does harness-session writes, and the `external_id` column exists in schema
but nothing ever populates it. Building this also means:
- Extending `memory_write` with optional `scope`/`source_type`/
  `external_id` (defaults preserve today's behavior for existing callers).
- Extending `memory_query` to support a pure structured/tag-only lookup
  mode (skip the semantic embedding+distance-cutoff path when no free-text
  query is given) — currently a real gap, useful beyond this feature too.
- A new unique index on `(source, external_id, scope)`.
- **New precedent for hivemind:** a built-in hourly background cleanup
  ticker inside `hivemindd` itself (soft-max retention on the raw log
  cache + its memory entries). Hivemind has had zero background/scheduled
  behavior until now — everything has been request-driven via MCP calls.

**Next step:** invoke the `superpowers:writing-plans` skill against
`docs/plans/2026-09-06-ci-log-ingestion-design.md` to turn this into a
task-by-task implementation plan (the brainstorming skill's own next-step
convention — see that skill if picking this up fresh). No approach
decisions are still open; the design doc's "Open items for the
implementation plan" section lists only implementation-level details
(exact thresholds, SQL upsert mechanics, ticker lifecycle wiring).

---

## Everything else about this project

Standing docs, unchanged by this session except where noted:
- `intent.md` — product vision.
- `docs/PRD.md` — requirements (Local + Cloud editions).
- `docs/DESIGN.md` — running log of resolved design/implementation
  questions, in chronological order. Read the tail end for everything
  from this session (Windows cgo toolchain, `zip` availability, token
  permissions, private-repo asset auth).
- `README.md` — now includes real install instructions (this session's
  Task 8), plus the pre-existing daemon usage/API docs.
