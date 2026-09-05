# Hivemind — CI/CD and Distribution Requirements

Requirements for continuous integration, release automation, and end-user
installation of `hivemindd`, captured before implementation.

---

## Goals

- Catch build/lint/test breakage on every PR, across every platform we ship.
- Cut a release by pushing a git tag, with no manual build steps.
- Make installation as close to one command as possible for macOS, Linux,
  and Windows, while the source repository stays private.

## Constraints

- `hivemindd` depends on cgo (`mattn/go-sqlite3`, `sqlite-vec-go-bindings`),
  so cross-compilation is not viable — each platform/arch must build on a
  matching native runner.
- `causewayai/hivemind` is a **private** GitHub repository and stays that
  way for this pass. Homebrew and Scoop both support private
  taps/buckets/releases via a GitHub personal access token, so this is a
  friction cost, not a blocker.
- No Apple code signing/notarization in this pass (no Apple Developer
  account in scope). macOS users will see a Gatekeeper warning on first run
  and need to clear it manually (`xattr -d com.apple.quarantine` or
  right-click → Open). Revisit if this project is ever made public.

---

## Platform matrix

| OS | Arch | CI | Release artifact | Install |
|---|---|---|---|---|
| macOS | arm64 | ✅ | `.tar.gz` | Homebrew tap |
| macOS | amd64 | ✅ | `.tar.gz` | Homebrew tap |
| Linux | amd64 | ✅ | `.tar.gz` | Homebrew tap (Linuxbrew) |
| Windows | amd64 | ✅ | `.zip` | Scoop bucket |

arm64 Linux/Windows and non-amd64 Windows are explicitly out of scope for
now — add later on demand.

---

## CI (pull request / push to `main`)

- **Trigger:** every PR and every push to `main`.
- **Matrix:** `macos-15` (arm64), `macos-15-intel` (amd64), `ubuntu-latest`
  (amd64), `windows-latest` (amd64) — same 4 legs as the release matrix, so
  a platform-specific cgo break surfaces before merge, not at tag time.
  (`macos-13` was fully retired by GitHub in December 2025 and `macos-14`
  is itself mid-deprecation as of this writing, retiring November 2026 —
  `macos-15`/`macos-15-intel` are the current stable labels.)
- **Per-leg steps:** checkout → set up Go 1.25 → ensure a C compiler is
  present (Xcode Command Line Tools on macOS, `gcc` on Linux, a
  mingw-w64/GCC toolchain on Windows — cgo here needs real GCC, not MSVC)
  → `make check` (`go vet` + `golangci-lint run` + `go test ./...`).
- **Branch protection:** all 4 legs required to pass before merge to
  `main`. (A repo setting to configure, not a workflow file.)

---

## Release pipeline (tag-triggered)

- **Trigger:** pushing a tag matching `v*.*.*` (semver, e.g. `v0.3.0`).
- **Tooling decision (revised):** GoReleaser's ability to merge native
  per-OS cgo builds into one release + one Homebrew/Scoop manifest requires
  **GoReleaser Pro** (paid) — its `split`/`continue --merge` orchestration
  and its `prebuilt` builder (needed to feed externally-built binaries into
  the `brews:`/`scoops:` pipes) are both Pro-only features, confirmed
  against GoReleaser's own docs. Decision: **hand-roll it** with plain
  `go build` per OS and a scripted final publish step — no GoReleaser
  dependency, no license cost, more workflow YAML/scripts to own.
- **Per-OS build jobs:** each of the 4 platform/arch legs runs `go build`
  directly with `-ldflags "-X main.version=... -X main.commit=... -X
  main.date=..."`, then archives the binary (`.tar.gz` for macOS/Linux,
  `.zip` for Windows) and uploads it as a workflow artifact.
- **Publish job:** a final `ubuntu-latest` job downloads all 4 archives,
  computes a combined `checksums.txt` (`sha256sum`), and creates the
  GitHub Release via `gh release create <tag> <archives...> checksums.txt
  --generate-notes` — `--generate-notes` gives an auto-generated changelog
  from merged PRs since the last tag, replacing GoReleaser's changelog pipe.
- **Version embedding:** version/commit/date injected via `-ldflags` into
  `main` package vars, surfaced through `hivemindd --version`, so an
  installed binary's provenance is verifiable.
- **Tap/bucket fan-out:** the publish job (or a job depending on it) reads
  each archive's sha256 from `checksums.txt`, renders the Homebrew formula
  and Scoop manifest from a template (simple string substitution — no
  templating library needed), and pushes the result as a commit to
  `causewayai/homebrew-causewayai` / `causewayai/scoop-hivemind` using a
  cross-repo PAT.

---

## Homebrew tap (macOS + Linux)

- **Repo:** `causewayai/homebrew-causewayai` (the `homebrew-` prefix is
  required by Homebrew's tap-discovery convention), private.
- **Formula:** `Formula/hivemindd.rb`, regenerated and pushed by GoReleaser
  on every release.
- **Service management:** the formula declares a `service do ... end`
  block (launchd `RunAtLoad`/`KeepAlive` on macOS; Homebrew-on-Linux maps
  the same block onto its systemd-backed service runner), so
  `brew services start|stop|restart hivemindd` works identically on both
  platforms — no manual foregrounding or hand-rolled launchd plist needed.
- **Install flow:**
  ```
  brew tap causewayai/causewayai
  brew install hivemindd
  brew services start hivemindd
  ```

## Scoop bucket (Windows)

- **Repo:** `causewayai/scoop-hivemind`, private. GoReleaser pushes an
  updated manifest JSON on every release.
- **Install flow:** `scoop bucket add hivemind ... && scoop install hivemindd`.
- **Service management:** out of scope. Scoop has no `brew services`
  equivalent; Windows users run the installed binary directly or wire up
  their own Task Scheduler/NSSM entry. Revisit if Windows daemon management
  becomes a real ask.

## Access (private repos throughout)

- **End users:** need a GitHub PAT with `repo` scope set as
  `HOMEBREW_GITHUB_API_TOKEN` (Homebrew) — Scoop reads the same kind of PAT
  via its own git-credential configuration for a private bucket — so that
  tap/bucket add, install, and upgrade can authenticate against the private
  tap/bucket repo and the private release assets. Document this as a
  one-time setup step in the README; it's the deliberate friction traded
  for keeping the source private.
- **CI:** the release workflow needs a token with write access to both
  `homebrew-causewayai` and `scoop-hivemind` — the default per-run
  `GITHUB_TOKEN` can't push to other repos — stored as a repo secret
  (`HOMEBREW_TAP_TOKEN`) in `causewayai/hivemind`.

---

## Non-goals (this pass)

- Publishing to `homebrew-core` (requires a public, notable project).
- winget submission (requires public repo + Microsoft review, or a
  self-hosted winget source).
- macOS code signing/notarization.
- arm64 Windows or Linux builds.
- Windows background-service management (Task Scheduler/NSSM wiring).

---

## Open items for the implementation plan

- Confirm which mingw-w64/GCC setup reliably builds `mattn/go-sqlite3` +
  `sqlite-vec-go-bindings` cgo on `windows-latest` — this is the
  least-proven leg of the whole matrix and should be spiked first (the
  GitHub-hosted Windows runner ships a MinGW GCC via its bundled Strawberry
  Perl install, at `C:\Strawberry\c\bin\gcc.exe`, which is the usual
  zero-install trick for Go cgo projects on `windows-latest` — verify it
  actually links these two cgo dependencies before relying on it, with
  `msys2/setup-msys2` as a fallback if it doesn't).
- Decide the exact `service do` block contents (log paths, working
  directory, restart policy) for the Homebrew formula.
