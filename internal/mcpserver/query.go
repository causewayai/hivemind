package mcpserver

import (
	"context"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryQueryInput is the memory_query tool's input schema. Read-only; never a
// write path.
//
// When Query is empty the daemon runs a structured-only lookup (no embedding,
// no semantic-distance cutoff): results are the entries matching the
// external_id / tags / source filters, newest first. This is the "do I already
// have run X cached?" path — see the CI log ingestion design doc.
type MemoryQueryInput struct {
	SessionID  string   `json:"session_id" jsonschema:"the calling harness's session identifier"`
	Query      string   `json:"query,omitempty" jsonschema:"free text for semantic search; omit for an exact structured lookup"`
	Tags       []string `json:"tags,omitempty"`
	Source     string   `json:"source,omitempty"`
	ExternalID string   `json:"external_id,omitempty" jsonschema:"exact ETL key match; only honored on a structured-only query (no free text)"`
	TopK       int      `json:"top_k,omitempty"`
}

// MemoryQueryOutput is the memory_query tool's output.
type MemoryQueryOutput struct {
	Results []*store.MemoryEntry `json:"results"`
}

func (s *Server) handleMemoryQuery(ctx context.Context, in MemoryQueryInput) (*MemoryQueryOutput, error) {
	if in.TopK <= 0 {
		in.TopK = 10
	}

	if in.Query == "" {
		return s.structuredQuery(in)
	}

	embVec, err := s.embedder.Embed(ctx, in.Query)
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
	own = append(own, shared...)
	if len(own) > in.TopK {
		own = own[:in.TopK]
	}
	return &MemoryQueryOutput{Results: own}, nil
}

// structuredQuery serves the no-free-text path: exact filters, newest first,
// same session-isolation rule as the semantic path (own session scope + all
// user scope).
func (s *Server) structuredQuery(in MemoryQueryInput) (*MemoryQueryOutput, error) {
	own, err := s.store.ListMemories(store.ListFilter{
		Scope: "session", SessionID: in.SessionID,
		Tags: in.Tags, Source: in.Source, ExternalID: in.ExternalID, Limit: in.TopK,
	})
	if err != nil {
		return nil, err
	}
	shared, err := s.store.ListMemories(store.ListFilter{
		Scope: "user",
		Tags:  in.Tags, Source: in.Source, ExternalID: in.ExternalID, Limit: in.TopK,
	})
	if err != nil {
		return nil, err
	}
	own = append(own, shared...)
	if len(own) > in.TopK {
		own = own[:in.TopK]
	}
	return &MemoryQueryOutput{Results: own}, nil
}
