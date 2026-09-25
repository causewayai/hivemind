package mcpserver

import (
	"context"
	"fmt"

	"github.com/causewayai/hivemind/internal/store"
)

// MemoryWriteInput is the memory_write tool's input schema.
//
// scope/source_type/external_id are optional and default to the historical
// harness-session behavior (scope="session", source_type="harness", no
// external_id). Supplying them enables the ETL upsert path — see
// docs/plans/2026-09-06-ci-log-ingestion-design.md, "Security tradeoff": in
// Local Edition any local caller may already write any scope, so exposing
// these here does not weaken a boundary that Local Edition enforces.
type MemoryWriteInput struct {
	SessionID  string    `json:"session_id" jsonschema:"the harness-generated session identifier; required when scope is session"`
	Content    string    `json:"content" jsonschema:"the memory content to store"`
	Source     string    `json:"source" jsonschema:"identifier of the writer (harness name, or ETL source like github-actions)"`
	Tags       []string  `json:"tags,omitempty" jsonschema:"optional labels for filtering"`
	Embedding  []float32 `json:"embedding,omitempty" jsonschema:"optional precomputed embedding; if omitted the daemon generates one"`
	Scope      string    `json:"scope,omitempty" jsonschema:"session (default) or user"`
	SourceType string    `json:"source_type,omitempty" jsonschema:"harness (default) or etl"`
	ExternalID string    `json:"external_id,omitempty" jsonschema:"optional ETL key; when set the write is an idempotent upsert on (source, external_id, scope)"`
}

// MemoryWriteOutput is the memory_write tool's output: the new (or existing) entry's ID.
type MemoryWriteOutput struct {
	ID string `json:"id"`
}

func (s *Server) handleMemoryWrite(ctx context.Context, in MemoryWriteInput) (*MemoryWriteOutput, error) {
	scope := in.Scope
	if scope == "" {
		scope = "session"
	}
	if scope != "session" && scope != "user" {
		return nil, fmt.Errorf("invalid scope %q: want session or user", scope)
	}
	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = "harness"
	}
	if sourceType != "harness" && sourceType != "etl" {
		return nil, fmt.Errorf("invalid source_type %q: want harness or etl", sourceType)
	}
	sessionID := in.SessionID
	if scope == "user" {
		sessionID = "" // user-scope entries are not tied to a session
	}

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
		Scope:      scope,
		SessionID:  sessionID,
		Source:     in.Source,
		SourceType: sourceType,
		ExternalID: in.ExternalID,
		Tags:       in.Tags,
		Embedding:  embVec,
	})
	if err != nil {
		return nil, err
	}
	return &MemoryWriteOutput{ID: entry.ID}, nil
}
