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
