// Package cilog holds the GitHub-Actions CI-log cache convention: the
// external_id and tag vocabulary plus repo/URL parsing shared by the hivemind
// CLI and the daemon's retention sweep (internal/cilog/retention). It is pure
// (stdlib only) so the client CLI can import it without linking the store's
// cgo. hivemindd's core never imports this as a "GitHub" concept — it only
// sees generic memory_write/memory_query.
package cilog

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Source is the memory `source` value for every CI-log entry.
const Source = "github-actions"

const logPathPrefix = "log_path:"

// RunExternalID is the composite key for a run-level summary entry.
func RunExternalID(repo, runID string) string { return fmt.Sprintf("%s#%s", repo, runID) }

// JobExternalID is the composite key for a per-failed-job entry.
func JobExternalID(repo, runID, jobID string) string {
	return fmt.Sprintf("%s#%s#%s", repo, runID, jobID)
}

// LogPathTag renders the tag that ties a memory entry to its raw log file.
func LogPathTag(absPath string) string { return logPathPrefix + absPath }

// PathFromLogPathTag is the inverse of LogPathTag.
func PathFromLogPathTag(tag string) (string, bool) {
	return strings.TrimPrefix(tag, logPathPrefix), strings.HasPrefix(tag, logPathPrefix)
}

// RunLogPath is where the full `gh run view --log` text for a run is cached.
func RunLogPath(dir, owner, repo, runID string) string {
	return filepath.Join(dir, owner, repo, runID, "run.log")
}

// Tags builds the standard tag set for a run-summary entry.
func Tags(repo, runID, workflow, commit, status string) []string {
	return []string{
		"repo:" + repo,
		"run_id:" + runID,
		"workflow:" + workflow,
		"commit:" + commit,
		"status:" + status,
	}
}
