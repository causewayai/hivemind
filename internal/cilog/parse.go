package cilog

import (
	"regexp"
	"strings"
)

var remoteRe = regexp.MustCompile(`github\.com[:/]+([^/]+/[^/]+?)(?:\.git)?/?$`)

// RepoFromRemoteURL extracts "owner/repo" from a github.com git remote URL.
func RepoFromRemoteURL(url string) (string, bool) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", false
	}
	return m[1], true
}
