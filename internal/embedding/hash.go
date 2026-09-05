package embedding

import (
	"context"
	"hash/fnv"
)

// HashProvider is a deterministic, non-semantic stand-in for a real
// embedding model. It exists so the daemon is runnable and testable
// end-to-end without any external model configured. It must be swapped
// for a real provider before semantic search results are meaningful.
type HashProvider struct {
	dim int
}

// NewHashProvider returns a HashProvider that produces dim-dimensional vectors.
func NewHashProvider(dim int) *HashProvider {
	return &HashProvider{dim: dim}
}

// Embed implements Provider. It ignores ctx: hashing is CPU-only and never blocks.
func (p *HashProvider) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, p.dim)
	h := fnv.New32a()
	for i := 0; i < p.dim; i++ {
		h.Write([]byte{byte(i)})
		h.Write([]byte(text))
		sum := h.Sum32()
		vec[i] = float32(sum%2000)/1000.0 - 1.0 // spread into [-1, 1)
	}
	return vec, nil
}
