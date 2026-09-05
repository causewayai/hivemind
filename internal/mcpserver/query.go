package mcpserver

import (
	"context"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryQueryInput is the memory_query tool's input schema.
// PRD "Interfaces -> MCP Interface": harnesses get read-only access to
// scopes beyond their own session, so this never exposes a write path.
type MemoryQueryInput struct {
	SessionID string   `json:"session_id" jsonschema:"the calling harness's session identifier"`
	Query     string   `json:"query,omitempty" jsonschema:"free text for semantic search"`
	Tags      []string `json:"tags,omitempty"`
	Source    string   `json:"source,omitempty"`
	TopK      int      `json:"top_k,omitempty"`
}

type MemoryQueryOutput struct {
	Results []*store.MemoryEntry `json:"results"`
}

func (s *Server) handleMemoryQuery(ctx context.Context, in MemoryQueryInput) (*MemoryQueryOutput, error) {
	if in.TopK <= 0 {
		in.TopK = 10
	}

	embVec, err := s.embedder.Embed(in.Query)
	if err != nil {
		return nil, err
	}

	own, err := s.store.Query(store.QueryInput{
		Embedding: embVec, Tags: in.Tags, Source: in.Source, TopK: in.TopK,
		Scope: "session", SessionID: in.SessionID,
	})
	if err != nil {
		return nil, err
	}

	shared, err := s.store.Query(store.QueryInput{
		Embedding: embVec, Tags: in.Tags, Source: in.Source, TopK: in.TopK,
		Scope: "user",
	})
	if err != nil {
		return nil, err
	}

	results := append(own, shared...)
	if len(results) > in.TopK {
		results = results[:in.TopK]
	}
	return &MemoryQueryOutput{Results: results}, nil
}
