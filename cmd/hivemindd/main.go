// Command hivemindd is the Hivemind Local Edition daemon: an MCP server,
// bound to loopback only, backed by SQLite + sqlite-vec.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/causewayai/hivemind/internal/cilog/retention"
	"github.com/causewayai/hivemind/internal/config"
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/mcpserver"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("hivemindd %s (commit %s, built %s)", version, commit, date)
}

func buildMCPServer(s *store.Store, embedder embedding.Provider) *mcp.Server {
	srv := mcpserver.New(s, embedder)
	server := mcp.NewServer(&mcp.Implementation{Name: "hivemind", Version: version}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_write", Description: "Write a memory entry"}, srv.HandleMemoryWrite)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_query", Description: "Query memories via semantic search and/or structured filters"}, srv.HandleMemoryQuery)
	mcp.AddTool(server, &mcp.Tool{Name: "list_scopes", Description: "List memory scopes available in this edition"}, srv.HandleListScopes)
	return server
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println(versionString())
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run wires up signal handling and configuration, then hands off to serve.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return serve(ctx, cfg)
}

// serve runs the daemon until ctx is cancelled, then shuts down gracefully.
func serve(ctx context.Context, cfg *config.Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s, err := store.Open(cfg.DataDir, cfg.EmbeddingDim)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = s.Close() }()

	embedder := embedding.NewHashProvider(cfg.EmbeddingDim)
	mcpSrv := buildMCPServer(s, embedder)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpSrv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	addr := "127.0.0.1:" + strconv.Itoa(cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if err := os.MkdirAll(cfg.RuntimeDir(), 0o755); err != nil {
		return fmt.Errorf("runtime dir: %w", err)
	}
	if err := os.WriteFile(cfg.PortFilePath(), []byte(strconv.Itoa(port)), 0o644); err != nil {
		return fmt.Errorf("write port file: %w", err)
	}
	_ = os.WriteFile(cfg.PIDFilePath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer func() {
		_ = os.Remove(cfg.PortFilePath())
		_ = os.Remove(cfg.PIDFilePath())
	}()

	if cfg.CILogCleanupEnabled {
		go retention.RunTicker(ctx, s, retention.TickerConfig{
			Dir: cfg.CILogDir, MaxAge: cfg.CILogMaxAge, MaxSize: cfg.CILogMaxSize, Interval: time.Hour,
		})
	}

	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("hivemindd %s listening on 127.0.0.1:%d (loopback only)", version, port)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
