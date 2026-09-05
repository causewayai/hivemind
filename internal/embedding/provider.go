// Package embedding defines the pluggable embedding-provider interface used
// by the daemon to convert freeform text into vectors for semantic search.
package embedding

import "context"

// Provider generates an embedding vector for freeform text. ctx carries
// cancellation/timeout for providers that call out over the network (see
// HashProvider for a non-network stand-in).
//
// A caller (harness) may also supply a precomputed vector directly when
// writing a memory, bypassing Provider entirely — see PRD "Retrieval":
// the harness's own LLM is a valid embedding source.
type Provider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}
