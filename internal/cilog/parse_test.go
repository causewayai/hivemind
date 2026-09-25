package cilog

import "testing"

func TestRepoFromRemoteURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"git@github.com:causewayai/hivemind.git", "causewayai/hivemind"},
		{"https://github.com/causewayai/hivemind.git", "causewayai/hivemind"},
		{"https://github.com/causewayai/hivemind", "causewayai/hivemind"},
	} {
		if got, _ := RepoFromRemoteURL(c.in); got != c.want {
			t.Errorf("RepoFromRemoteURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, ok := RepoFromRemoteURL("file:///tmp/x"); ok {
		t.Error("non-github remote should not parse")
	}
}
