// Package retention is the store-dependent CI-log cache retention sweep and
// hourly ticker. It lives apart from internal/cilog — which stays pure
// (stdlib only) — so the hivemind client CLI can import the CI-log vocabulary
// without transitively linking internal/store and its sqlite-vec cgo
// dependency. Only hivemindd imports this package.
package retention

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/causewayai/hivemind/internal/cilog"
	"github.com/causewayai/hivemind/internal/store"
)

// SweepResult reports what a Sweep removed.
type SweepResult struct {
	FilesDeleted   int
	BytesDeleted   int64
	EntriesDeleted int
}

type logFile struct {
	path    string
	size    int64
	modTime time.Time
}

// Sweep enforces the retention policy on the raw-log cache rooted at dir:
// delete every *.log older than maxAge, then, if the remaining total still
// exceeds maxSize, delete oldest-first until it doesn't. For every log file
// removed it also deletes the memory entries tagged log_path:<that file>, so a
// summary/failure entry never outlives its backing log.
//
// A non-positive limit means "no cap": maxAge <= 0 skips the age-expiry pass
// entirely and maxSize <= 0 skips the size-cap pass entirely, so a mis-set
// negative environment value can never purge the whole cache. A missing dir
// is not an error — Sweep walks nothing and returns a zero result.
func Sweep(s *store.Store, dir string, maxAge time.Duration, maxSize int64, now time.Time) (SweepResult, error) {
	var res SweepResult

	var files []logFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".log" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, logFile{path: path, size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return res, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })

	var kept []logFile
	var keptSize int64
	if maxAge > 0 {
		cutoff := now.Add(-maxAge)
		for _, f := range files {
			if f.modTime.Before(cutoff) {
				if err := deleteLog(s, f, &res); err != nil {
					return res, err
				}
				continue
			}
			kept = append(kept, f)
			keptSize += f.size
		}
	} else {
		kept = files
		for _, f := range files {
			keptSize += f.size
		}
	}

	if maxSize > 0 {
		for _, f := range kept {
			if keptSize <= maxSize {
				break
			}
			if err := deleteLog(s, f, &res); err != nil {
				return res, err
			}
			keptSize -= f.size
		}
	}
	return res, nil
}

func deleteLog(s *store.Store, f logFile, res *SweepResult) error {
	entries, err := s.ListMemories(store.ListFilter{Tags: []string{cilog.LogPathTag(f.path)}})
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.DeleteMemory(e.ID); err != nil {
			return err
		}
		res.EntriesDeleted++
	}
	if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	res.FilesDeleted++
	res.BytesDeleted += f.size
	return nil
}
