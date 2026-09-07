package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runtimeDir is the directory holding the daemon's runtime files. It MUST stay
// in sync with config.Config.RuntimeDir() in internal/config: the parent of
// HIVEMIND_DATA_DIR, else ~/.hivemind.
func runtimeDir() string {
	if v := os.Getenv("HIVEMIND_DATA_DIR"); v != "" {
		return filepath.Dir(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".hivemind"
	}
	return filepath.Join(home, ".hivemind")
}

func portFilePath() string { return filepath.Join(runtimeDir(), "daemon.port") }
func pidFilePath() string  { return filepath.Join(runtimeDir(), "daemon.pid") }

// readDaemonPort returns the port hivemindd advertised, or an error if the
// file is absent or unparsable.
func readDaemonPort() (int, error) {
	b, err := os.ReadFile(portFilePath())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// dialDaemon opens an MCP session to the already-running daemon. It does NOT
// start one — see ensureDaemon (Task 12).
func dialDaemon(ctx context.Context) (*mcp.ClientSession, error) {
	port, err := readDaemonPort()
	if err != nil {
		return nil, fmt.Errorf("daemon not discoverable: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hivemind-cli", Version: version}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d/", port),
	}, nil)
}

// daemonRunning reports whether a dial currently succeeds.
func daemonRunning(ctx context.Context) bool {
	sess, err := dialDaemon(ctx)
	if err != nil {
		return false
	}
	_ = sess.Close()
	return true
}

// serverVersion returns the daemon's advertised implementation version, or ""
// if the SDK does not surface it. The value comes from the cached initialize
// result, so it is safe to read after the session is closed.
func serverVersion(sess *mcp.ClientSession) string {
	res := sess.InitializeResult()
	if res == nil || res.ServerInfo == nil {
		return ""
	}
	return res.ServerInfo.Version
}

// connectChecked returns a session to a running (auto-started if needed)
// daemon, restarting it once if it is older than this CLI.
func connectChecked(ctx context.Context) (*mcp.ClientSession, error) {
	sess, err := ensureDaemon(ctx)
	if err != nil {
		return nil, err
	}
	dv := serverVersion(sess)
	if daemonOlderThanCLI(version, dv) {
		_ = sess.Close()
		fmt.Fprintf(os.Stderr, "hivemind: running hivemindd %s is older than CLI %s; restarting it\n", dv, version)
		if code := stopDaemon(); code != 0 {
			return nil, fmt.Errorf("could not restart the older daemon automatically; stop it yourself (e.g. `brew services restart hivemindd`) and retry")
		}
		return ensureDaemon(ctx)
	}
	return sess, nil
}
