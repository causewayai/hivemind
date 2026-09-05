package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "")
	t.Setenv("HIVEMIND_PORT", "")
	t.Setenv("HIVEMIND_EMBEDDING_DIM", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8420 {
		t.Errorf("Port = %d, want 8420", cfg.Port)
	}
	if cfg.EmbeddingDim != 768 {
		t.Errorf("EmbeddingDim = %d, want 768", cfg.EmbeddingDim)
	}
	if cfg.DataDir == "" {
		t.Errorf("DataDir is empty, want a default path under the user's home directory")
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "/tmp/hivemind-test")
	t.Setenv("HIVEMIND_PORT", "9000")
	t.Setenv("HIVEMIND_EMBEDDING_DIM", "1536")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/tmp/hivemind-test" {
		t.Errorf("DataDir = %q, want /tmp/hivemind-test", cfg.DataDir)
	}
	if cfg.Port != 9000 {
		t.Errorf("Port = %d, want 9000", cfg.Port)
	}
	if cfg.EmbeddingDim != 1536 {
		t.Errorf("EmbeddingDim = %d, want 1536", cfg.EmbeddingDim)
	}
}
