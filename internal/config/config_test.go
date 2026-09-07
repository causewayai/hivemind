package config

import (
	"testing"
	"time"
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

func TestConfig_RuntimeAndFilePaths(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "/tmp/hm/hivemind.db")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RuntimeDir() != "/tmp/hm" {
		t.Errorf("RuntimeDir() = %q, want /tmp/hm", cfg.RuntimeDir())
	}
	if cfg.PortFilePath() != "/tmp/hm/daemon.port" {
		t.Errorf("PortFilePath() = %q", cfg.PortFilePath())
	}
	if cfg.PIDFilePath() != "/tmp/hm/daemon.pid" {
		t.Errorf("PIDFilePath() = %q", cfg.PIDFilePath())
	}
}

func TestConfig_CILogDefaults(t *testing.T) {
	t.Setenv("HIVEMIND_DATA_DIR", "/tmp/hm/hivemind.db")
	for _, k := range []string{"HIVEMIND_CI_LOG_DIR", "HIVEMIND_CI_LOG_MAX_AGE", "HIVEMIND_CI_LOG_MAX_SIZE", "HIVEMIND_CI_LOG_CLEANUP"} {
		t.Setenv(k, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CILogDir != "/tmp/hm/ci-logs" {
		t.Errorf("CILogDir = %q", cfg.CILogDir)
	}
	if cfg.CILogMaxAge != 30*24*time.Hour {
		t.Errorf("CILogMaxAge = %v, want 720h", cfg.CILogMaxAge)
	}
	if cfg.CILogMaxSize != 500*1024*1024 {
		t.Errorf("CILogMaxSize = %d", cfg.CILogMaxSize)
	}
	if !cfg.CILogCleanupEnabled {
		t.Error("CILogCleanupEnabled should default true")
	}
}

func TestConfig_CILogOverrides(t *testing.T) {
	t.Setenv("HIVEMIND_CI_LOG_MAX_AGE", "48h")
	t.Setenv("HIVEMIND_CI_LOG_MAX_SIZE", "1048576")
	t.Setenv("HIVEMIND_CI_LOG_CLEANUP", "off")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CILogMaxAge != 48*time.Hour {
		t.Errorf("CILogMaxAge = %v", cfg.CILogMaxAge)
	}
	if cfg.CILogMaxSize != 1<<20 {
		t.Errorf("CILogMaxSize = %d", cfg.CILogMaxSize)
	}
	if cfg.CILogCleanupEnabled {
		t.Error("HIVEMIND_CI_LOG_CLEANUP=off must disable cleanup")
	}
}
