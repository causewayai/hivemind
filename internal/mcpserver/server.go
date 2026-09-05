// Package mcpserver implements the MCP tool handlers (memory_write,
// memory_query, list_scopes) that back the Local Edition daemon.
package mcpserver

import (
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
)

// Server holds the dependencies shared by all MCP tool handlers.
type Server struct {
	store    *store.Store
	embedder embedding.Provider
}

// New constructs a Server backed by the given store and embedding provider.
func New(s *store.Store, embedder embedding.Provider) *Server {
	return &Server{store: s, embedder: embedder}
}
