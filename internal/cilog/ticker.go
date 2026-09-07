package cilog

import (
	"context"
	"log"
	"time"

	"github.com/causewayai/hivemind/internal/store"
)

// TickerConfig parameterizes RunTicker. Interval is injectable so tests need
// not wait an hour; the daemon passes time.Hour.
type TickerConfig struct {
	Dir      string
	MaxAge   time.Duration
	MaxSize  int64
	Interval time.Duration
}

// RunTicker runs one retention Sweep immediately, then again every
// cfg.Interval, until ctx is cancelled. Sweep errors are logged, not fatal.
func RunTicker(ctx context.Context, s *store.Store, cfg TickerConfig) {
	sweep := func() {
		res, err := Sweep(s, cfg.Dir, cfg.MaxAge, cfg.MaxSize, time.Now())
		if err != nil {
			log.Printf("ci-log sweep error: %v", err)
			return
		}
		if res.FilesDeleted > 0 {
			log.Printf("ci-log sweep: removed %d files (%d bytes), %d memory entries",
				res.FilesDeleted, res.BytesDeleted, res.EntriesDeleted)
		}
	}

	sweep()
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
