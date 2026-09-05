package embedding

// Provider generates an embedding vector for freeform text.
// A caller (harness) may also supply a precomputed vector directly when
// writing a memory, bypassing Provider entirely — see PRD "Retrieval":
// the harness's own LLM is a valid embedding source.
type Provider interface {
	Embed(text string) ([]float32, error)
}
