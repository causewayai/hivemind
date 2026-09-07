package main

import (
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/mcpserver"
	"github.com/causewayai/hivemind/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testMCPServer builds an in-process MCP server backed by s, matching the tool
// set the real daemon exposes. Used by the CLI's connection tests.
func testMCPServer(s *store.Store, e embedding.Provider) *mcp.Server {
	srv := mcpserver.New(s, e)
	m := mcp.NewServer(&mcp.Implementation{Name: "hivemind", Version: "test"}, nil)
	mcp.AddTool(m, &mcp.Tool{Name: "memory_write"}, srv.HandleMemoryWrite)
	mcp.AddTool(m, &mcp.Tool{Name: "memory_query"}, srv.HandleMemoryQuery)
	mcp.AddTool(m, &mcp.Tool{Name: "list_scopes"}, srv.HandleListScopes)
	return m
}
