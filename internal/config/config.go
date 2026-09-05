package config

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	DataDir      string
	Port         int
	EmbeddingDim int
}

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
