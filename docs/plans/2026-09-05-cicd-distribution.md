# CI/CD and Homebrew/Scoop Distribution Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Give `hivemindd` a CI gate across macOS/Linux/Windows and a
tag-triggered release pipeline that publishes binaries to GitHub Releases
and updates a private Homebrew tap (macOS + Linux) and a private Scoop
bucket (Windows), with `brew services` support for running the daemon.

**Architecture:** No GoReleaser (its multi-OS-merge and prebuilt-binary
features are Pro-only — see `docs/plans/2026-09-05-cicd-distribution-design.md`).
CI and release both run as GitHub Actions matrices of native
`go build`/`go test` jobs (one per OS/arch, since cgo blocks cross-compilation).
The release workflow adds a final Linux job that collects all 4 platform
archives, cuts the GitHub Release with `gh release create --generate-notes`,
and pushes a hand-templated Homebrew formula / Scoop manifest to two new
private sibling repos using a cross-repo PAT.

**Tech Stack:** GitHub Actions, Go 1.25 (cgo), `gh` CLI, plain shell/`sha256sum`
for checksums, a Homebrew tap repo (`causewayai/homebrew-causewayai`) and a
Scoop bucket repo (`causewayai/scoop-causewayai`).

---

## Before you start

Read `docs/plans/2026-09-05-cicd-distribution-design.md` in full — it
records every decision this plan implements and *why* (private repo,
platform matrix, no code signing, no GoReleaser, `brew services` support,
etc.). Don't relitigate those decisions; if one seems wrong, raise it with
the user rather than silently deviating.

Several tasks below create real GitHub resources (two new repos, a
repo secret, a pushed release tag) or ask the user to create a credential
(a PAT). These are called out explicitly — **stop and confirm with the
user before taking that specific action**, per this project's standing
rule about hard-to-reverse or shared-state changes. Everything else
(editing files, opening a PR, pushing a branch) is normal, low-risk
development work and does not need a check-in.

---

## Part A — Version embedding in the binary

### Task 1: Add `--version` support to `hivemindd`

**Files:**
- Modify: `cmd/hivemindd/main.go`
- Test: `cmd/hivemindd/main_test.go`

**Step 1: Write the failing test**

Add to `cmd/hivemindd/main_test.go`:

```go
func TestVersionString(t *testing.T) {
	version = "1.2.3"
	commit = "abc1234"
	date = "2026-09-05T00:00:00Z"
	defer func() { version, commit, date = "dev", "none", "unknown" }()

	got := versionString()
	want := "hivemindd 1.2.3 (commit abc1234, built 2026-09-05T00:00:00Z)"
	if got != want {
		t.Errorf("versionString() = %q, want %q", got, want)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/hivemindd/... -run TestVersionString -v`
Expected: FAIL — `undefined: version` (or `versionString`)

**Step 3: Write minimal implementation**

In `cmd/hivemindd/main.go`, add package-level vars (set via `-ldflags` at
build time, defaulting to placeholders for local `go build`/`go run`) and
a `versionString` helper, then handle `--version`/`-version` in `main`
before config loading:

```go
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("hivemindd %s (commit %s, built %s)", version, commit, date)
}
```

In `main()`, before calling `run()`:

```go
func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println(versionString())
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
```

Add `"os"` to the import block.

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/hivemindd/... -run TestVersionString -v`
Expected: PASS

**Step 5: Run full test suite + lint**

Run: `make check`
Expected: all pass (this also confirms the new `os` import and unused-var
lint rules are satisfied)

**Step 6: Commit**

```bash
git add cmd/hivemindd/main.go cmd/hivemindd/main_test.go
git commit -m "feat: add --version flag with ldflags-injected build metadata"
```

---

## Part B — CI workflow (PR/push gate)

### Task 2: Add the CI workflow

**Files:**
- Create: `.github/workflows/ci.yml`

**Step 1: Write the workflow**

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [main]

jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        include:
          - os: macos-15
            name: macos-arm64
          - os: macos-15-intel
            name: macos-amd64
          - os: ubuntu-latest
            name: linux-amd64
          - os: windows-latest
            name: windows-amd64
    runs-on: ${{ matrix.os }}
    name: test (${{ matrix.name }})
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'

      - name: Configure MinGW GCC (Windows only)
        if: runner.os == 'Windows'
        run: echo "C:\Strawberry\c\bin" >> $env:GITHUB_PATH

      - name: Verify C compiler
        run: go env CC && (go env CC | xargs which 2>/dev/null || where $(go env CC))
        shell: bash

      - name: Install golangci-lint
        run: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

      - name: make check
        run: make check
        shell: bash
```

Note the `Verify C compiler` step is a deliberate early-failure diagnostic:
if cgo can't find a compiler, `make check`'s build step fails with a less
obvious error further down — this step surfaces it immediately with a
clear message. `make check` runs with `shell: bash` on Windows because
`Makefile`s aren't natively invocable from PowerShell without a `make`
that understands POSIX shell rules; GitHub's `windows-latest` runner
ships Git Bash and `make` via its Git for Windows / Chocolatey base image.

**Step 2: Verify `make` and a C compiler are actually available on
`windows-latest`**

This is the one genuinely unproven leg (flagged in the design doc). Push
this workflow on a branch and open a draft PR to trigger it, rather than
trying to verify locally (there's no Windows machine in this dev loop):

```bash
git checkout -b ci/add-workflow
git add .github/workflows/ci.yml
git commit -m "ci: add cross-platform build/lint/test workflow"
git push -u origin ci/add-workflow
gh pr create --draft --title "ci: add cross-platform CI workflow" --body "Verifying the Windows cgo build leg."
```

Then watch the run:

```bash
gh pr checks --watch
```

**Step 3: If the Windows leg fails on missing `make` or a broken cgo
compiler, apply this fallback and re-push**

Replace the `Configure MinGW GCC` step with an MSYS2-based toolchain and
run the Go commands directly instead of through `make`:

```yaml
      - name: Set up MSYS2 (Windows only)
        if: runner.os == 'Windows'
        uses: msys2/setup-msys2@v2
        with:
          install: mingw-w64-x86_64-gcc make

      - name: make check (Windows)
        if: runner.os == 'Windows'
        shell: msys2 {0}
        run: make check

      - name: make check (macOS/Linux)
        if: runner.os != 'Windows'
        shell: bash
        run: make check
```

Iterate with `gh pr checks --watch` until all 4 legs are green. Whichever
variant ends up working, note it in `docs/DESIGN.md` under a new
"Windows CI cgo toolchain" heading, following the existing entries' style
(what was tried, what worked, why).

**Step 4: Once all 4 legs pass, mark the PR ready and merge**

```bash
gh pr ready
gh pr merge --squash
```

**Step 5: Confirm with the user before changing branch protection**

Ask the user to add a required-status-checks rule on `main` requiring all
4 `test (…)` matrix jobs — this is a repo setting change, not something to
apply silently. Point them to Settings → Branches → Branch protection
rules, or offer to run
`gh api repos/causewayai/hivemind/branches/main/protection ...` yourself
if they'd rather you do it, listing the exact contexts first.

---

## Part C — Homebrew tap and Scoop bucket repos

### Task 3: Create the two distribution repos

**This creates new GitHub repositories — confirm with the user before
running these commands.**

```bash
gh repo create causewayai/homebrew-causewayai --private \
  --description "Homebrew tap for hivemindd" 
gh repo create causewayai/scoop-causewayai --private \
  --description "Scoop bucket for hivemindd"
```

**Step 1: Seed the tap repo with a placeholder formula**

Clone it locally (a temp directory is fine) and add
`Formula/hivemindd.rb` with a placeholder the release workflow will
overwrite on the first real release:

```ruby
class Hivemindd < Formula
  desc "Local persistent memory daemon for AI harnesses (MCP over HTTP)"
  homepage "https://github.com/causewayai/hivemind"
  version "0.0.0"
  license "UNLICENSED"

  on_macos do
    on_arm do
      url "https://github.com/causewayai/hivemind/releases/download/v0.0.0/hivemindd_darwin_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000"
    end
    on_intel do
      url "https://github.com/causewayai/hivemind/releases/download/v0.0.0/hivemindd_darwin_amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000"
    end
  end

  on_linux do
    url "https://github.com/causewayai/hivemind/releases/download/v0.0.0/hivemindd_linux_amd64.tar.gz"
    sha256 "0000000000000000000000000000000000000000000000000000000000000"
  end

  def install
    bin.install "hivemindd"
  end

  service do
    run [opt_bin/"hivemindd"]
    keep_alive true
    log_path var/"log/hivemindd.log"
    error_log_path var/"log/hivemindd.log"
    working_dir var
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/hivemindd --version")
  end
end
```

Commit and push this placeholder to `main` on `homebrew-causewayai`.

**Step 2: Seed the bucket repo with a placeholder manifest**

Add `bucket/hivemindd.json` to `scoop-causewayai`:

```json
{
  "version": "0.0.0",
  "description": "Local persistent memory daemon for AI harnesses (MCP over HTTP)",
  "homepage": "https://github.com/causewayai/hivemind",
  "license": "UNLICENSED",
  "architecture": {
    "64bit": {
      "url": "https://github.com/causewayai/hivemind/releases/download/v0.0.0/hivemindd_windows_amd64.zip",
      "hash": "0000000000000000000000000000000000000000000000000000000000000"
    }
  },
  "bin": "hivemindd.exe",
  "checkver": {
    "github": "https://github.com/causewayai/hivemind"
  }
}
```

Commit and push this placeholder to `main` on `scoop-causewayai`.

**Step 3: Confirm both repos exist and have the placeholder file**

```bash
gh api repos/causewayai/homebrew-causewayai/contents/Formula/hivemindd.rb --jq .name
gh api repos/causewayai/scoop-causewayai/contents/bucket/hivemindd.json --jq .name
```
Expected: each prints the filename, confirming the push landed.

---

### Task 4: Set up the cross-repo PAT

**This asks the user to create a credential — do not attempt to automate
PAT creation; GitHub doesn't expose an API for minting new PATs on a
user's behalf.**

**Step 1: Ask the user to create a fine-grained PAT**

Tell the user: go to
https://github.com/settings/personal-access-tokens/new, create a
fine-grained token scoped to the `causewayai` org, restricted to the two
repos `homebrew-causewayai` and `scoop-causewayai`, with **Contents:
Read and write** permission. Suggest no expiration shorter than the
project's realistic release cadence (e.g. 1 year), since a silently
expired token turns into a broken release pipeline.

**Step 2: Add it as a secret on the main repo**

Once the user gives you the token value (or pastes it directly into a
`gh secret set` prompt so it never appears in chat/history):

```bash
gh secret set HOMEBREW_TAP_TOKEN --repo causewayai/hivemind
```//paste the token at the prompt

**Step 3: Verify the secret is set (not its value)**

```bash
gh secret list --repo causewayai/hivemind
```
Expected: `HOMEBREW_TAP_TOKEN` appears in the list.

---

## Part D — Release workflow

### Task 5: Add the per-OS build + archive jobs

**Files:**
- Create: `.github/workflows/release.yml`

**Step 1: Write the build matrix portion**

```yaml
name: Release

on:
  push:
    tags:
      - 'v*.*.*'

jobs:
  build:
    strategy:
      fail-fast: false
      matrix:
        include:
          - os: macos-15
            goos: darwin
            goarch: arm64
            archive: tar.gz
          - os: macos-15-intel
            goos: darwin
            goarch: amd64
            archive: tar.gz
          - os: ubuntu-latest
            goos: linux
            goarch: amd64
            archive: tar.gz
          - os: windows-latest
            goos: windows
            goarch: amd64
            archive: zip
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'

      - name: Configure MinGW GCC (Windows only)
        if: runner.os == 'Windows'
        run: echo "C:\Strawberry\c\bin" >> $env:GITHUB_PATH

      - name: Build
        shell: bash
        env:
          CGO_ENABLED: 1
        run: |
          VERSION="${GITHUB_REF_NAME#v}"
          COMMIT="$(git rev-parse --short HEAD)"
          DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
          EXT=""
          [ "${{ matrix.goos }}" = "windows" ] && EXT=".exe"
          go build -ldflags "-X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
            -o "hivemindd${EXT}" ./cmd/hivemindd

      - name: Archive (tar.gz)
        if: matrix.archive == 'tar.gz'
        run: tar -czf "hivemindd_${{ matrix.goos }}_${{ matrix.goarch }}.tar.gz" hivemindd

      - name: Archive (zip)
        if: matrix.archive == 'zip'
        shell: bash
        run: |
          zip "hivemindd_${{ matrix.goos }}_${{ matrix.goarch }}.zip" hivemindd.exe

      - uses: actions/upload-artifact@v4
        with:
          name: hivemindd_${{ matrix.goos }}_${{ matrix.goarch }}
          path: hivemindd_${{ matrix.goos }}_${{ matrix.goarch }}.*
          if-no-files-found: error
```

Match this build matrix's `goos`/`goarch`/`os` triples exactly to the CI
workflow's (Task 2) — if a platform is ever added or removed, both files
need the change together, along with the formula/manifest templates in
Task 6.

**Step 2: Commit (this task alone doesn't need a real tag push to
verify — Task 7 exercises the full pipeline)**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add per-OS release build/archive jobs"
```

---

### Task 6: Add the publish job (GitHub Release + tap/bucket update)

**Files:**
- Modify: `.github/workflows/release.yml`

**Step 1: Append the publish job**

```yaml
  publish:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/download-artifact@v4
        with:
          path: dist
          merge-multiple: true

      - name: Checksums
        working-directory: dist
        run: sha256sum hivemindd_* > checksums.txt

      - name: Create GitHub Release
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          gh release create "${GITHUB_REF_NAME}" dist/* \
            --title "${GITHUB_REF_NAME}" \
            --generate-notes

      - name: Compute per-asset checksums for tap/bucket
        working-directory: dist
        run: |
          echo "DARWIN_ARM64_SHA=$(grep darwin_arm64 checksums.txt | awk '{print $1}')" >> "$GITHUB_ENV"
          echo "DARWIN_AMD64_SHA=$(grep darwin_amd64 checksums.txt | awk '{print $1}')" >> "$GITHUB_ENV"
          echo "LINUX_AMD64_SHA=$(grep linux_amd64 checksums.txt | awk '{print $1}')" >> "$GITHUB_ENV"
          echo "WINDOWS_AMD64_SHA=$(grep windows_amd64 checksums.txt | awk '{print $1}')" >> "$GITHUB_ENV"

      - name: Update Homebrew tap
        env:
          GH_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
        run: |
          VERSION="${GITHUB_REF_NAME#v}"
          git clone "https://x-access-token:${GH_TOKEN}@github.com/causewayai/homebrew-causewayai.git" tap
          cat > tap/Formula/hivemindd.rb <<EOF
          class Hivemindd < Formula
            desc "Local persistent memory daemon for AI harnesses (MCP over HTTP)"
            homepage "https://github.com/causewayai/hivemind"
            version "${VERSION}"
            license "UNLICENSED"

            on_macos do
              on_arm do
                url "https://github.com/causewayai/hivemind/releases/download/${GITHUB_REF_NAME}/hivemindd_darwin_arm64.tar.gz"
                sha256 "${DARWIN_ARM64_SHA}"
              end
              on_intel do
                url "https://github.com/causewayai/hivemind/releases/download/${GITHUB_REF_NAME}/hivemindd_darwin_amd64.tar.gz"
                sha256 "${DARWIN_AMD64_SHA}"
              end
            end

            on_linux do
              url "https://github.com/causewayai/hivemind/releases/download/${GITHUB_REF_NAME}/hivemindd_linux_amd64.tar.gz"
              sha256 "${LINUX_AMD64_SHA}"
            end

            def install
              bin.install "hivemindd"
            end

            service do
              run [opt_bin/"hivemindd"]
              keep_alive true
              log_path var/"log/hivemindd.log"
              error_log_path var/"log/hivemindd.log"
              working_dir var
            end

            test do
              assert_match version.to_s, shell_output("#{bin}/hivemindd --version")
            end
          end
          EOF
          cd tap
          git config user.name "hivemind-release-bot"
          git config user.email "actions@github.com"
          git add Formula/hivemindd.rb
          git commit -m "hivemindd ${VERSION}"
          git push

      - name: Update Scoop bucket
        env:
          GH_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
        run: |
          VERSION="${GITHUB_REF_NAME#v}"
          git clone "https://x-access-token:${GH_TOKEN}@github.com/causewayai/scoop-causewayai.git" bucket
          cat > bucket/bucket/hivemindd.json <<EOF
          {
            "version": "${VERSION}",
            "description": "Local persistent memory daemon for AI harnesses (MCP over HTTP)",
            "homepage": "https://github.com/causewayai/hivemind",
            "license": "UNLICENSED",
            "architecture": {
              "64bit": {
                "url": "https://github.com/causewayai/hivemind/releases/download/${GITHUB_REF_NAME}/hivemindd_windows_amd64.zip",
                "hash": "${WINDOWS_AMD64_SHA}"
              }
            },
            "bin": "hivemindd.exe",
            "checkver": {
              "github": "https://github.com/causewayai/hivemind"
            }
          }
          EOF
          cd bucket
          git config user.name "hivemind-release-bot"
          git config user.email "actions@github.com"
          git add bucket/hivemindd.json
          git commit -m "hivemindd ${VERSION}"
          git push
```

Note the reused secret name: `HOMEBREW_TAP_TOKEN` grants write access to
*both* `homebrew-causewayai` and `scoop-causewayai` (Task 4 scoped the PAT to
both repos), so the Scoop step reuses it rather than needing a second
secret.

**Step 2: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add release-publish job (GitHub Release, tap, bucket)"
```

**Step 3: Open a PR, get CI green, merge to main**

```bash
git push -u origin ci/add-workflow   # or a new branch if Task 2's branch is already merged
gh pr create --title "ci: add release pipeline" --body "Tag-triggered build/publish workflow. See docs/plans/2026-09-05-cicd-distribution.md."
gh pr checks --watch
gh pr merge --squash
```

---

## Part E — End-to-end verification

### Task 7: Cut a real test release

**This pushes a real tag, creates a real GitHub Release, and pushes
commits to the two tap/bucket repos — confirm with the user before
running this.**

**Step 1: Tag and push**

Use an obviously-a-test version so it's easy to identify and clean up:

```bash
git checkout main && git pull
git tag v0.0.1-test
git push origin v0.0.1-test
```

**Step 2: Watch the release workflow**

```bash
gh run watch --exit-status
```
Expected: `build` matrix (4 jobs) and `publish` all succeed.

**Step 3: Verify the GitHub Release**

```bash
gh release view v0.0.1-test --repo causewayai/hivemind
```
Expected: 4 archives + `checksums.txt` attached, auto-generated notes
present.

**Step 4: Verify the Homebrew formula updated**

```bash
gh api repos/causewayai/homebrew-causewayai/contents/Formula/hivemindd.rb --jq '.content' | base64 -d | grep version
```
Expected: `version "0.0.1-test"`

**Step 5: Verify the Scoop manifest updated**

```bash
gh api repos/causewayai/scoop-causewayai/contents/bucket/hivemindd.json --jq '.content' | base64 -d | jq .version
```
Expected: `"0.0.1-test"`

**Step 6: Do a real local install test on this machine (macOS)**

```bash
brew tap causewayai/causewayai
brew install hivemindd
hivemindd --version
brew services start hivemindd
brew services list | grep hivemindd
brew services stop hivemindd
```
Expected: `--version` prints `hivemindd 0.0.1-test (commit ..., built ...)`;
`brew services list` shows `hivemindd` as `started`.

If this machine doesn't have `HOMEBREW_GITHUB_API_TOKEN` set for the
private tap, set it first per the README instructions written in Task 8.

**Step 7: Clean up the test release**

Ask the user before deleting anything. If they confirm:

```bash
gh release delete v0.0.1-test --repo causewayai/hivemind --yes
git push origin :refs/tags/v0.0.1-test
git tag -d v0.0.1-test
brew uninstall hivemindd
```

Leave the tap/bucket repos' formula/manifest pointing at `0.0.1-test` —
the next real release overwrites it, and there's no harm in a stale
pre-1.0 test version sitting there in the meantime; deleting the GitHub
Release itself is what matters (it removes the actual binaries).

---

## Part F — Documentation

### Task 8: Update the README with install instructions

**Files:**
- Modify: `README.md`

**Step 1: Replace the "Install / Build" section**

Keep the existing "build from source" instructions (still needed for
contributors) but add an install-from-package-manager path above it:

```markdown
## Install

`hivemindd` is distributed via a private Homebrew tap (macOS + Linux) and
a private Scoop bucket (Windows). Both require a GitHub personal access
token since the repos are private — ask a maintainer for org access, then:

**One-time setup (macOS/Linux):**

```bash
export HOMEBREW_GITHUB_API_TOKEN=<your PAT with read access to causewayai>
```

Add that line to your shell profile so it persists across sessions.

### macOS / Linux (Homebrew)

```bash
brew tap causewayai/causewayai
brew install hivemindd
brew services start hivemindd   # runs hivemindd in the background at login
```

Check it's running: `brew services list`. Stop it with
`brew services stop hivemindd`.

### Windows (Scoop)

```powershell
scoop bucket add hivemind https://github.com/causewayai/scoop-causewayai
scoop install hivemindd
```

Scoop doesn't manage background services — run `hivemindd` directly, or
wire it into Task Scheduler yourself.

### Build from source

[... existing content ...]
```

**Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add Homebrew/Scoop install instructions"
```

---

## After this plan

- Windows service management (`brew services`-equivalent) — deferred, no
  request for it yet (see design doc non-goals).
- macOS code signing/notarization — deferred until/unless the project
  goes public.
- arm64 Linux/Windows builds — add a matrix leg to both workflows and a
  new `on_macos`/`architecture` entry in the formula/manifest templates
  if ever requested.
