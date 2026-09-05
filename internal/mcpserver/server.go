package mcpserver

import (
	"github.com/causewayai/hivemind/internal/embedding"
	"github.com/causewayai/hivemind/internal/store"
)

type Server struct {
	store    *store.Store
	embedder embedding.Provider
}

func New(s *store.Store, embedder embedding.Provider) *Server {
	return &Server{store: s, embedder: embedder}
}
