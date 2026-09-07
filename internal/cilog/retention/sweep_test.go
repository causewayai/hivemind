package retention

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/causewayai/hivemind/internal/store"
)

// statMissing returns nil when path does not exist, else os.ErrExist.
func statMissing(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.ErrExist
}

func writeLog(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func seedEntry(t *testing.T, s *store.Store, extID, logPath string) {
	t.Helper()
	if _, err := s.CreateMemory(store.CreateMemoryInput{
		Content: "x", Scope: "user", Source: cilog.Source, SourceType: "etl",
		ExternalID: extID, Tags: []string{cilog.LogPathTag(logPath)},
		Embedding: make([]float32, 8),
	}); err != nil {
		t.Fatalf("seed error = %v", err)
	}
}

func TestSweep_DeletesByAgeAndCascadesEntries(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	old := filepath.Join(dir, "o", "r", "1", "run.log")
	fresh := filepath.Join(dir, "o", "r", "2", "run.log")
	writeLog(t, old, 10, 48*time.Hour)
	writeLog(t, fresh, 10, 1*time.Hour)
	seedEntry(t, s, "o/r#1", old)
	seedEntry(t, s, "o/r#2", fresh)

	res, err := Sweep(s, dir, 24*time.Hour, 1<<30, time.Now())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if res.FilesDeleted != 1 || res.EntriesDeleted != 1 {
		t.Fatalf("Sweep() = %+v, want 1 file + 1 entry deleted", res)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old log still present")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh log wrongly deleted")
	}
	if e, _ := s.GetMemoryByExternalID(cilog.Source, "o/r#1", "user"); e != nil {
		t.Error("entry for old log not cascaded")
	}
	if e, _ := s.GetMemoryByExternalID(cilog.Source, "o/r#2", "user"); e == nil {
		t.Error("entry for fresh log wrongly deleted")
	}
}

func TestSweep_EvictsOldestUntilUnderSizeCap(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	a := filepath.Join(dir, "o", "r", "1", "run.log") // oldest
	b := filepath.Join(dir, "o", "r", "2", "run.log")
	c := filepath.Join(dir, "o", "r", "3", "run.log") // newest
	writeLog(t, a, 200, 72*time.Hour)
	writeLog(t, b, 200, 48*time.Hour)
	writeLog(t, c, 200, 1*time.Hour)
	seedEntry(t, s, "o/r#1", a)
	seedEntry(t, s, "o/r#2", b)
	seedEntry(t, s, "o/r#3", c)

	// maxAge huge (nothing age-expires); each log is 200 bytes, so the cache
	// holds 600. Size cap 250 -> evict oldest-first (600 -> 400 -> 200) until
	// under cap: the two oldest go, the newest survives.
	res, err := Sweep(s, dir, 365*24*time.Hour, 250, time.Now())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if res.FilesDeleted != 2 {
		t.Fatalf("Sweep() FilesDeleted = %d, want 2", res.FilesDeleted)
	}
	if _, err := os.Stat(c); err != nil {
		t.Error("newest log should survive the size sweep")
	}
}

func TestSweep_NonPositiveLimitsAreNoOp(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	a := filepath.Join(dir, "o", "r", "1", "run.log")
	b := filepath.Join(dir, "o", "r", "2", "run.log")
	writeLog(t, a, 100, 1*time.Hour)
	writeLog(t, b, 100, 2*time.Hour)
	seedEntry(t, s, "o/r#1", a)
	seedEntry(t, s, "o/r#2", b)

	res, err := Sweep(s, dir, -1, -1, time.Now())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if res.FilesDeleted != 0 || res.EntriesDeleted != 0 {
		t.Fatalf("Sweep() = %+v, want a pure no-op for non-positive limits", res)
	}
	if _, err := os.Stat(a); err != nil {
		t.Error("log a wrongly deleted under non-positive limits")
	}
	if _, err := os.Stat(b); err != nil {
		t.Error("log b wrongly deleted under non-positive limits")
	}
	if e, _ := s.GetMemoryByExternalID(cilog.Source, "o/r#1", "user"); e == nil {
		t.Error("entry for log a wrongly deleted under non-positive limits")
	}
	if e, _ := s.GetMemoryByExternalID(cilog.Source, "o/r#2", "user"); e == nil {
		t.Error("entry for log b wrongly deleted under non-positive limits")
	}
}
