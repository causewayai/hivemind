package mcpserver

import (
	"context"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryWriteInput is the memory_write tool's input schema.
// PRD "Interfaces -> MCP Interface": harnesses have full CRUD on their own
// session scope only — writing is therefore always scoped to SessionID,
// there is no Scope field to override that.
type MemoryWriteInput struct {
	SessionID string    `json:"session_id" jsonschema:"the harness-generated session identifier"`
	Content   string    `json:"content" jsonschema:"the memory content to store"`
	Source    string    `json:"source" jsonschema:"identifier of the harness writing this memory"`
	Tags      []string  `json:"tags,omitempty" jsonschema:"optional labels for filtering"`
	Embedding []float32 `json:"embedding,omitempty" jsonschema:"optional precomputed embedding; if omitted the daemon generates one"`
}

// MemoryWriteOutput is the memory_write tool's output: the new entry's ID.
type MemoryWriteOutput struct {
	ID string `json:"id"`
}

func (s *Server) handleMemoryWrite(ctx context.Context, in MemoryWriteInput) (*MemoryWriteOutput, error) {
	embVec := in.Embedding
	if embVec == nil {
		var err error
		embVec, err = s.embedder.Embed(ctx, in.Content)
		if err != nil {
			return nil, err
		}
	}

	entry, err := s.store.CreateMemory(store.CreateMemoryInput{
		Content:    in.Content,
		Scope:      "session",
		SessionID:  in.SessionID,
		Source:     in.Source,
		SourceType: "harness",
		Tags:       in.Tags,
		Embedding:  embVec,
	})
	if err != nil {
		return nil, err
	}
	return &MemoryWriteOutput{ID: entry.ID}, nil
}
