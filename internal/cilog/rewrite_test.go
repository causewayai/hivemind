package cilog

import "testing"

func TestRewriteGHLogCommand(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		matched bool
	}{
		{`gh run view 42 --log -R o/r`, `hivemind ci-logs run view 42 --log -R o/r`, true},
		{`gh run view --log-failed 42`, `hivemind ci-logs run view --log-failed 42`, true},
		{`gh run view 42 --json conclusion`, ``, false},
		{`gh run list`, ``, false},
		{`gh pr view 3`, ``, false},
		{`echo gh run view 42 --log`, ``, false},
		{`gh run view 42 --log && rm -rf /`, ``, false},
		{`gh run view 42 --log | tee x`, ``, false},
		{`hivemind ci-logs run view 42 --log`, ``, false},
	}
	for _, c := range cases {
		got, ok := RewriteGHLogCommand(c.in)
		if ok != c.matched || got != c.want {
			t.Errorf("RewriteGHLogCommand(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.matched)
		}
	}
}
