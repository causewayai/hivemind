package cilog

import "testing"

func TestExternalIDs(t *testing.T) {
	if got := RunExternalID("o/r", "42"); got != "o/r#42" {
		t.Errorf("RunExternalID = %q", got)
	}
	if got := JobExternalID("o/r", "42", "99"); got != "o/r#42#99" {
		t.Errorf("JobExternalID = %q", got)
	}
}

func TestLogPathTag(t *testing.T) {
	if got := LogPathTag("/a/b.log"); got != "log_path:/a/b.log" {
		t.Errorf("LogPathTag = %q", got)
	}
	p, ok := PathFromLogPathTag("log_path:/a/b.log")
	if !ok || p != "/a/b.log" {
		t.Errorf("PathFromLogPathTag = %q, %v", p, ok)
	}
	if _, ok := PathFromLogPathTag("repo:o/r"); ok {
		t.Error("non log_path tag must not parse")
	}
}
