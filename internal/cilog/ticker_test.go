package cilog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/causewayai/hivemind/internal/store"
)

func TestRunTicker_SweepsOnStartThenStopsOnCancel(t *testing.T) {
	s, err := store.Open(t.TempDir()+"/db.sqlite", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dir := t.TempDir()

	old := filepath.Join(dir, "o", "r", "1", "run.log")
	writeLog(t, old, 10, 72*time.Hour)
	seedEntry(t, s, "o/r#1", old)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunTicker(ctx, s, TickerConfig{Dir: dir, MaxAge: time.Hour, MaxSize: 1 << 30, Interval: time.Hour})
		close(done)
	}()

	// The immediate startup sweep must remove the stale file well before the first tick.
	deadline := time.After(2 * time.Second)
	for {
		if err := statMissing(old); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("startup sweep did not run within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunTicker did not return after context cancel")
	}
}
