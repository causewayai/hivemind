package cilog

import (
	"strings"
	"testing"
)

func TestBuildRunSummary(t *testing.T) {
	m := RunMeta{
		Repo: "o/r", RunID: "42", WorkflowName: "CI", Conclusion: "failure",
		HeadSHA: "abc123", HeadBranch: "main", Event: "push",
		Jobs: []JobMeta{{DatabaseID: 7, Name: "build", Conclusion: "failure"}, {DatabaseID: 8, Name: "lint", Conclusion: "success"}},
	}
	got := BuildRunSummary(m)
	for _, want := range []string{"o/r", "run 42", "CI", "failure", "abc123", "main", "push", "build"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lint") {
		t.Errorf("successful job should not be listed as failed:\n%s", got)
	}
}

func TestExtractJobFailure(t *testing.T) {
	log := strings.Join([]string{
		"build\t2026-09-06T00:00:01Z line one",
		"build\t2026-09-06T00:00:02Z ##[error]zip: command not found",
		"build\t2026-09-06T00:00:03Z after error",
		"lint\t2026-09-06T00:00:01Z unrelated",
	}, "\n")
	got := ExtractJobFailure(log, "build")
	if !strings.Contains(got, "zip: command not found") {
		t.Errorf("expected the error line, got:\n%s", got)
	}
	if strings.Contains(got, "unrelated") {
		t.Errorf("must not include other jobs' lines:\n%s", got)
	}
}
