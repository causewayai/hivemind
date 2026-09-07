package main

import (
	"strconv"
	"strings"
)

// daemonOlderThanCLI reports whether the daemon's semver is strictly older
// than the CLI's. Non-semver on either side (e.g. "dev") disables the check
// and returns false.
func daemonOlderThanCLI(cli, daemon string) bool {
	c, ok1 := parseSemver(cli)
	d, ok2 := parseSemver(daemon)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if d[i] != c[i] {
			return d[i] < c[i]
		}
	}
	return false
}

// parseSemver parses "MAJOR.MINOR.PATCH" (a leading "v" and any pre-release or
// build suffix are tolerated), returning the numeric triple and whether it
// parsed.
func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
