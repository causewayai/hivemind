package embedding

import (
	"context"
	"testing"
)

func TestHashProvider_Deterministic(t *testing.T) {
	p := NewHashProvider(768)
	ctx := context.Background()

	v1, err := p.Embed(ctx, "hello world")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	v2, err := p.Embed(ctx, "hello world")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if len(v1) != 768 {
		t.Errorf("len(v1) = %d, want 768", len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("HashProvider not deterministic at index %d: %f != %f", i, v1[i], v2[i])
		}
	}
}
