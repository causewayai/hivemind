package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type cilogsDeps struct {
	stdout io.Writer
	gh     func(ctx context.Context, args ...string) ([]byte, error)
}

func defaultCILogsDeps() cilogsDeps {
	return cilogsDeps{
		stdout: os.Stdout,
		gh: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "gh", args...).Output()
		},
	}
}

func runCILogs(args []string) int {
	return runCILogsWith(context.Background(), defaultCILogsDeps(), args)
}

type cilogsArgs struct {
	runID     string
	repo      string
	logFailed bool
	logAll    bool
}

func parseCILogsArgs(args []string) (cilogsArgs, error) {
	if len(args) < 3 || args[0] != "run" || args[1] != "view" {
		return cilogsArgs{}, fmt.Errorf("usage: hivemind ci-logs run view <run-id> [-R owner/repo] [--log|--log-failed]")
	}
	var a cilogsArgs
	a.runID = args[2]
	fs := flag.NewFlagSet("ci-logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.repo, "R", "", "")
	fs.StringVar(&a.repo, "repo", "", "")
	fs.BoolVar(&a.logAll, "log", false, "")
	fs.BoolVar(&a.logFailed, "log-failed", false, "")
	if err := fs.Parse(args[3:]); err != nil {
		return cilogsArgs{}, err
	}
	return a, nil
}

func resolveRepo(ctx context.Context, d cilogsDeps, flagRepo string) (string, error) {
	if flagRepo != "" {
		return flagRepo, nil
	}
	if out, err := d.gh(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			return r, nil
		}
	}
	if out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output(); err == nil {
		if r, ok := cilog.RepoFromRemoteURL(string(out)); ok {
			return r, nil
		}
	}
	return "", fmt.Errorf("could not determine repo; pass -R owner/repo")
}

func runCILogsWith(ctx context.Context, d cilogsDeps, args []string) int {
	a, err := parseCILogsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 2
	}
	repo, err := resolveRepo(ctx, d, a.repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 2
	}

	sess, err := connectChecked(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
		return 1
	}
	defer func() { _ = sess.Close() }()

	extID := cilog.RunExternalID(repo, a.runID)
	hits, err := queryByExternalID(ctx, sess, extID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivemind: cache query failed: %v\n", err)
		return 1
	}
	if len(hits) > 0 {
		for _, h := range hits {
			if _, err := fmt.Fprintln(d.stdout, h); err != nil {
				fmt.Fprintf(os.Stderr, "hivemind: %v\n", err)
				return 1
			}
		}
		return 0
	}

	return cacheMiss(ctx, d, sess, repo, a)
}

// queryByExternalID runs a structured-only memory_query and returns entry contents.
func queryByExternalID(ctx context.Context, sess *mcp.ClientSession, extID string) ([]string, error) {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_query",
		Arguments: map[string]any{
			"session_id":  "hivemind-cli",
			"external_id": extID,
			"source":      cilog.Source,
			"top_k":       50,
		},
	})
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("memory_query returned an error result")
	}
	raw := mustJSON(res.StructuredContent)
	if len(res.Content) > 0 && (res.StructuredContent == nil || string(raw) == "null") {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			raw = []byte(tc.Text)
		}
	}
	var out struct {
		Results []struct {
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	contents := make([]string, 0, len(out.Results))
	for _, r := range out.Results {
		contents = append(contents, r.Content)
	}
	return contents, nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// cacheMiss is implemented in Task 16.
func cacheMiss(ctx context.Context, d cilogsDeps, sess *mcp.ClientSession, repo string, a cilogsArgs) int {
	_ = ctx
	_ = d
	_ = sess
	_ = repo
	_ = a
	fmt.Fprintln(os.Stderr, "hivemind: cache miss handling not yet implemented")
	return 1
}
