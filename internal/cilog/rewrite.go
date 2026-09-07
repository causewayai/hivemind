package cilog

import "strings"

// unsafeShell reports constructs we refuse to rewrite around, so the hook
// never "launders" a compound command into an auto-allowed rewrite. Mirrors
// rtk's contains_unattestable_construct, simplified.
func unsafeShell(cmd string) bool {
	for _, tok := range []string{"&&", "||", "|", ";", "\n", "$(", "`", ">", "<", "&"} {
		if strings.Contains(cmd, tok) {
			return true
		}
	}
	return false
}

// RewriteGHLogCommand returns the `hivemind ci-logs …` equivalent of a
// `gh run view … --log[-failed] …` command, and true, when cmd is exactly
// such a fetch (leading command, no shell metacharacters). Otherwise ("", false).
func RewriteGHLogCommand(cmd string) (string, bool) {
	trimmed := strings.TrimSpace(cmd)
	if unsafeShell(trimmed) {
		return "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 3 || fields[0] != "gh" || fields[1] != "run" || fields[2] != "view" {
		return "", false
	}
	// Only the id-first form (`gh run view <id> …`) rewrites cleanly.
	// parseCILogsArgs takes args[2] as the run id verbatim, so a flag-first
	// command (`gh run view --log 42`) would rewrite to one the CLI
	// mis-parses (run id "--log"). Refuse it: the hook emits nothing and the
	// original gh command runs unchanged.
	if len(fields) < 4 || strings.HasPrefix(fields[3], "-") {
		return "", false
	}
	hasLog := false
	for _, f := range fields[3:] {
		if f == "--log" || f == "--log-failed" {
			hasLog = true
			break
		}
	}
	if !hasLog {
		return "", false
	}
	return "hivemind ci-logs run view " + strings.Join(fields[3:], " "), true
}
