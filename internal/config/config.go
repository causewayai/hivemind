// Package config loads the daemon's runtime configuration from environment
// variables, applying defaults where unset.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds the daemon's runtime settings.
type Config struct {
	DataDir      string
	Port         int
	EmbeddingDim int

	CILogDir            string
	CILogMaxAge         time.Duration
	CILogMaxSize        int64
	CILogCleanupEnabled bool
}

// RuntimeDir is the directory holding the daemon's runtime files (port file,
// pid file, lock file, logs, ci-log cache) — the parent of the database file.
func (c *Config) RuntimeDir() string { return filepath.Dir(c.DataDir) }

// PortFilePath is where the daemon records the TCP port it bound.
func (c *Config) PortFilePath() string { return filepath.Join(c.RuntimeDir(), "daemon.port") }

// PIDFilePath is where the daemon records its process ID.
func (c *Config) PIDFilePath() string { return filepath.Join(c.RuntimeDir(), "daemon.pid") }

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

	cfg.CILogDir = filepath.Join(cfg.RuntimeDir(), "ci-logs")
	cfg.CILogMaxAge = 30 * 24 * time.Hour
	cfg.CILogMaxSize = 500 * 1024 * 1024
	cfg.CILogCleanupEnabled = true

	if v := os.Getenv("HIVEMIND_CI_LOG_DIR"); v != "" {
		cfg.CILogDir = v
	}
	if v := os.Getenv("HIVEMIND_CI_LOG_MAX_AGE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("HIVEMIND_CI_LOG_MAX_AGE: %w", err)
		}
		cfg.CILogMaxAge = d
	}
	if v := os.Getenv("HIVEMIND_CI_LOG_MAX_SIZE"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("HIVEMIND_CI_LOG_MAX_SIZE: %w", err)
		}
		cfg.CILogMaxSize = n
	}
	if strings.EqualFold(os.Getenv("HIVEMIND_CI_LOG_CLEANUP"), "off") {
		cfg.CILogCleanupEnabled = false
	}

	return cfg, nil
}
