package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type cilogsDeps struct {
	stdout io.Writer
	gh     func(ctx context.Context, args ...string) ([]byte, error)
}

func defaultCILogsDeps() cilogsDeps {
	return cilogsDeps{
		stdout: os.Stdout,
		gh: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "gh", args...).Output()
		},
	}
}

func runCILogs(args []string) int {
	return runCILogsWith(context.Background(), defaultCILogsDeps(), args)
}

type cilogsArgs struct {
	runID     string
	repo      string
	logFailed bool
	logAll    bool
}

func parseCILogsArgs(args []string) (cilogsArgs, error) {
	if len(args) < 3 || args[0] != "run" || args[1] != "view" {
		return cilogsArgs{}, fmt.Errorf("usage: hivemind ci-logs run view <run-id> [-R owner/repo] [--log|--log-failed]")
	}
	var a cilogsArgs
	a.runID = args[2]
	fs := flag.NewFlagSet("ci-logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.repo, "R", "", "")
	fs.StringVar(&a.repo, "repo", "", "")
	fs.BoolVar(&a.logAll, "log", false, "")
	fs.BoolVar(&a.logFailed, "log-failed", false, "")
	if err := fs.Parse(args[3:]); err != nil {
		return cilogsArgs{}, err
	}
	return a, nil
}

func resolveRepo(ctx context.Context, d cilogsDeps, flagRepo string) (string, error) {
	if flagRepo != "" {
		return flagRepo, nil
	}
	if out, err := d.gh(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			return r, nil
		}
	}
	if out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output(); err == nil {
		if r, ok := cilog.RepoFromRemoteURL(string(out)); ok {
			return r, nil
		}
	}
	return "", fmt.Errorf("could not determine repo; pass -R owner/repo")
}

func runCILogsWith(ctx context.Context, d cilogsDeps, args []string) int {
	a, err := parseCILogsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 2
	}
	repo, err := resolveRepo(ctx, d, a.repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 2
	}

	sess, err := connectChecked(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	defer func() { _ = sess.Close() }()

	entries, err := queryRunEntries(ctx, sess, repo, a.runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: cache query failed: %v\n", err)
		return 1
	}
	if len(entries) > 0 {
		var summary, jobs []cachedEntry
		for _, e := range entries {
			if hasJobTag(e.Tags) {
				jobs = append(jobs, e)
			} else {
				summary = append(summary, e)
			}
		}

		var parts []string
		if a.logFailed {
			// Only the per-failed-job entries; fall back to the summary
			// when the run recorded no isolated job failures.
			src := jobs
			if len(src) == 0 {
				src = summary
			}
			for _, e := range src {
				parts = append(parts, e.Content)
			}
		} else {
			for _, e := range summary {
				parts = append(parts, e.Content)
			}
			for _, e := range jobs {
				parts = append(parts, e.Content)
			}
		}

		if _, err := io.WriteString(d.stdout, strings.Join(parts, "\n\n")+"\n"); err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
			return 1
		}
		return 0
	}

	return cacheMiss(ctx, d, sess, repo, a)
}

// cachedEntry is the subset of a memory_query result the CLI needs. The daemon
// serializes store.MemoryEntry with capitalized keys; encoding/json matches
// these lowercase tags case-insensitively.
type cachedEntry struct {
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

// queryRunEntries runs a structured-only memory_query keyed on the shared
// run_id: tag and returns every cached entry for repo/runID — the run summary
// and one per failed job. The daemon's tag filter is ANY-of, so results are
// re-filtered client-side to entries carrying BOTH repo:<repo> and
// run_id:<runID>.
func queryRunEntries(ctx context.Context, sess *mcp.ClientSession, repo, runID string) ([]cachedEntry, error) {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_query",
		Arguments: map[string]any{
			"session_id": "hivemind-cli",
			"tags":       []string{"run_id:" + runID},
			"source":     cilog.Source,
			"top_k":      200,
		},
	})
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("memory_query returned an error result")
	}
	raw := mustJSON(res.StructuredContent)
	if len(res.Content) > 0 && (res.StructuredContent == nil || string(raw) == "null") {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			raw = []byte(tc.Text)
		}
	}
	var out struct {
		Results []cachedEntry `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	wantRepo, wantRun := "repo:"+repo, "run_id:"+runID
	matched := make([]cachedEntry, 0, len(out.Results))
	for _, e := range out.Results {
		if containsTag(e.Tags, wantRepo) && containsTag(e.Tags, wantRun) {
			matched = append(matched, e)
		}
	}
	return matched, nil
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// hasJobTag reports whether tags contains any "job:<name>" tag, marking the
// entry as a per-failed-job entry rather than the run summary.
func hasJobTag(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, "job:") {
			return true
		}
	}
	return false
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// cacheMiss fetches the run's logs and metadata via gh, caches the raw log to
// disk, writes a run-summary memory entry plus one entry per failed job, and
// finally echoes gh's raw --log output to stdout unchanged.
func cacheMiss(ctx context.Context, d cilogsDeps, sess *mcp.ClientSession, repo string, a cilogsArgs) int {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		fmt.Fprintf(os.Stderr, "hivemind: bad repo %q\n", repo)
		return 2
	}

	logFlag := "--log"
	if a.logFailed {
		logFlag = "--log-failed"
	}
	logOut, err := d.gh(ctx, "run", "view", a.runID, "-R", repo, logFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: gh log fetch failed: %v\n", err)
		return 1
	}
	metaOut, err := d.gh(ctx, "run", "view", a.runID, "-R", repo, "--json",
		"workflowName,conclusion,headSha,headBranch,event,jobs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: gh metadata fetch failed: %v\n", err)
		return 1
	}
	var meta cilog.RunMeta
	if err := json.Unmarshal(metaOut, &meta); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: parsing gh metadata: %v\n", err)
		return 1
	}
	meta.Repo, meta.RunID = repo, a.runID

	cacheDir := os.Getenv("HIVEMIND_CI_LOG_DIR")
	if cacheDir == "" {
		cacheDir = filepath.Join(runtimeDir(), "ci-logs")
	}
	runLog := cilog.RunLogPath(cacheDir, owner, name, a.runID)
	if err := os.MkdirAll(filepath.Dir(runLog), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	if err := os.WriteFile(runLog, logOut, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}

	summaryTags := append(cilog.Tags(repo, a.runID, meta.WorkflowName, meta.HeadSHA, meta.StatusWord()), cilog.LogPathTag(runLog))
	if err := writeEntry(ctx, sess, writeArgs{
		content:    cilog.BuildRunSummary(meta),
		externalID: cilog.RunExternalID(repo, a.runID),
		tags:       summaryTags,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: caching summary: %v\n", err)
		return 1
	}
	for _, j := range meta.FailedJobs() {
		body := cilog.ExtractJobFailure(string(logOut), j.Name)
		if body == "" {
			body = fmt.Sprintf("job %q failed (no error lines isolated; see %s)", j.Name, runLog)
		}
		if err := writeEntry(ctx, sess, writeArgs{
			content:    body,
			externalID: cilog.JobExternalID(repo, a.runID, itoa64(j.DatabaseID)),
			tags: []string{
				"repo:" + repo, "run_id:" + a.runID, "job:" + j.Name,
				"status:fail", cilog.LogPathTag(runLog),
			},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "hivemind: caching job failure: %v\n", err)
			return 1
		}
	}

	if _, err := d.stdout.Write(logOut); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	return 0
}

type writeArgs struct {
	content    string
	externalID string
	tags       []string
}

func writeEntry(ctx context.Context, sess *mcp.ClientSession, w writeArgs) error {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_write",
		Arguments: map[string]any{
			"session_id":  "hivemind-cli",
			"content":     w.content,
			"source":      cilog.Source,
			"scope":       "user",
			"source_type": "etl",
			"external_id": w.externalID,
			"tags":        w.tags,
		},
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("memory_write error result")
	}
	return nil
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
