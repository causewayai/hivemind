// Package config loads the daemon's runtime configuration from environment
// variables, applying defaults where unset.
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config holds the daemon's runtime settings.
type Config struct {
	DataDir      string
	Port         int
	EmbeddingDim int
}

// Load reads Config from HIVEMIND_DATA_DIR, HIVEMIND_PORT, and
// HIVEMIND_EMBEDDING_DIM, falling back to defaults for any unset variable.
func Load() (*Config, error) {
	cfg := &Config{
		Port:         8420,
		EmbeddingDim: 768,
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cfg.DataDir = filepath.Join(home, ".hivemind", "hivemind.db")

	if v := os.Getenv("HIVEMIND_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("HIVEMIND_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return nil, err
		}
		cfg.Port = port
	}
	if v := os.Getenv("HIVEMIND_EMBEDDING_DIM"); v != "" {
		dim, err := strconv.Atoi(v)
		if err != nil {
			return nil, err
		}
		cfg.EmbeddingDim = dim
	}

	return cfg, nil
}
