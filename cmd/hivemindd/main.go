// Command hivemindd is the Hivemind Local Edition daemon: an MCP server,
// bound to loopback only, backed by SQLite + sqlite-vec.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

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

	server := mcp.NewServer(&mcp.Implementation{Name: "hivemind", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_write", Description: "Write a memory entry, scoped to the calling session"}, srv.HandleMemoryWrite)
	mcp.AddTool(server, &mcp.Tool{Name: "memory_query", Description: "Query memories via semantic search and/or tag filters"}, srv.HandleMemoryQuery)
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

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

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
	log.Printf("hivemindd listening on %s (loopback only)", addr)
	return http.ListenAndServe(addr, handler)
}
