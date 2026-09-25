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

Put the run id first: `run view <run-id> --log`, not `run view --log <run-id>`.
