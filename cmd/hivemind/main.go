// Command hivemind is the client CLI for the Hivemind Local Edition daemon.
// It talks to a local hivemindd over MCP (loopback HTTP), starting one on
// demand if none is running. Its first real feature is `ci-logs`, a
// cache-first front for GitHub Actions log fetches.
package main

import (
	"fmt"
	"os"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("hivemind %s (commit %s, built %s)", version, commit, date)
}

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-version":
		fmt.Println(versionString())
		return 0
	case "ci-logs":
		return runCILogs(args[1:])
	case "daemon":
		return runDaemonCmd(args[1:])
	case "hook":
		return runHook(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hivemind: unknown subcommand %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage: hivemind <command> [args]

commands:
  ci-logs   fetch CI logs cache-first (see: hivemind ci-logs -h)
  daemon    manage the local hivemindd (start|stop|status)
  hook      Claude Code PreToolUse integration (claude|install|print)
  version   print version`
