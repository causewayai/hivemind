package cilog

import (
	"fmt"
	"strings"
)

// RunMeta is the subset of `gh run view --json ...` this cache needs.
type RunMeta struct {
	Repo         string    `json:"-"`
	RunID        string    `json:"-"`
	WorkflowName string    `json:"workflowName"`
	Conclusion   string    `json:"conclusion"`
	HeadSHA      string    `json:"headSha"`
	HeadBranch   string    `json:"headBranch"`
	Event        string    `json:"event"`
	Jobs         []JobMeta `json:"jobs"`
}

// JobMeta is one job from `gh run view --json jobs`.
type JobMeta struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// FailedJobs returns only the jobs whose conclusion is not "success".
func (m RunMeta) FailedJobs() []JobMeta {
	var out []JobMeta
	for _, j := range m.Jobs {
		if j.Conclusion != "" && j.Conclusion != "success" {
			out = append(out, j)
		}
	}
	return out
}

// StatusWord is "pass" or "fail" for the tag vocabulary.
func (m RunMeta) StatusWord() string {
	if m.Conclusion == "success" {
		return "pass"
	}
	return "fail"
}

// BuildRunSummary renders the run-level summary entry's content.
func BuildRunSummary(m RunMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s run %s — %s\n", m.Repo, m.RunID, m.Conclusion)
	fmt.Fprintf(&b, "workflow: %s\nbranch: %s\ncommit: %s\nevent: %s\n",
		m.WorkflowName, m.HeadBranch, m.HeadSHA, m.Event)
	if fj := m.FailedJobs(); len(fj) > 0 {
		names := make([]string, len(fj))
		for i, j := range fj {
			names[i] = j.Name
		}
		fmt.Fprintf(&b, "failed jobs: %s\n", strings.Join(names, ", "))
	}
	return b.String()
}

// ExtractJobFailure pulls the lines belonging to job jobName out of a
// `gh run view --log` blob (whose lines are "<job>\t<timestamp> <text>"),
// trimmed to a window around the first error marker.
func ExtractJobFailure(log, jobName string) string {
	var lines []string
	for _, ln := range strings.Split(log, "\n") {
		if strings.HasPrefix(ln, jobName+"\t") {
			lines = append(lines, ln)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	errIdx := -1
	for i, ln := range lines {
		if strings.Contains(ln, "##[error]") || strings.Contains(strings.ToLower(ln), "error") {
			errIdx = i
			break
		}
	}
	if errIdx < 0 {
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		return strings.Join(lines, "\n")
	}
	lo, hi := errIdx-10, errIdx+10
	if lo < 0 {
		lo = 0
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	return strings.Join(lines[lo:hi], "\n")
}
