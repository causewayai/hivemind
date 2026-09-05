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
- **Matrix:** `macos-latest` (arm64), `macos-13` (amd64), `ubuntu-latest`
  (amd64), `windows-latest` (amd64) — same 4 legs as the release matrix, so
  a platform-specific cgo break surfaces before merge, not at tag time.
- **Per-leg steps:** checkout → set up Go 1.25 → ensure a C compiler is
  present (Xcode Command Line Tools on macOS, `gcc` on Linux, a
  mingw-w64/GCC toolchain on Windows — cgo here needs real GCC, not MSVC)
  → `make check` (`go vet` + `golangci-lint run` + `go test ./...`).
- **Branch protection:** all 4 legs required to pass before merge to
  `main`. (A repo setting to configure, not a workflow file.)

---

## Release pipeline (tag-triggered)

- **Trigger:** pushing a tag matching `v*.*.*` (semver, e.g. `v0.3.0`).
- **Tooling:** [GoReleaser](https://goreleaser.com/), driven by one
  `.goreleaser.yml`. Because cgo blocks cross-compilation, the release
  workflow runs **one GoReleaser job per native OS/arch runner** using
  GoReleaser's partial-build mode (`--single-target`), then a final job
  merges the partial builds into one GitHub Release — GoReleaser's
  documented pattern for cgo-heavy projects.
- **Artifacts:** per platform/arch, a `.tar.gz` (macOS/Linux) or `.zip`
  (Windows) containing the `hivemindd` binary, plus a combined
  `checksums.txt` for the release as a whole.
- **Version embedding:** version, commit, and build date injected via
  `-ldflags` into a `main.version`-style variable, surfaced through
  `hivemindd --version`, so an installed binary's provenance is verifiable.
- **Changelog:** GoReleaser's default changelog generation from commit
  messages between tags, attached to the GitHub Release notes.
- **Tap/bucket fan-out:** the final release job updates the Homebrew
  formula and Scoop manifest (below) with the new version and per-asset
  checksums, via GoReleaser's native `brews:` and `scoops:` config blocks,
  which push a commit to each target repo directly.

---

## Homebrew tap (macOS + Linux)

- **Repo:** `causewayai/homebrew-hivemind` (the `homebrew-` prefix is
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
  brew tap causewayai/hivemind
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
  `homebrew-hivemind` and `scoop-hivemind` — the default per-run
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

- Confirm which mingw-w64/GCC setup action reliably builds
  `mattn/go-sqlite3` + `sqlite-vec-go-bindings` cgo on `windows-latest` —
  this is the least-proven leg of the whole matrix and should be spiked
  first.
- Confirm GoReleaser's multi-job partial-build-then-merge pattern works
  cleanly with three separate OS runners in one GitHub Actions workflow
  (job dependencies, artifact passing between jobs).
- Decide the exact `service do` block contents (log paths, working
  directory, restart policy) for the Homebrew formula.
