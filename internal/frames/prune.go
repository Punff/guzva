// Package frames: retention pass (PLAN §6.3). One SQL select + file deletes,
// run hourly by the scheduler (§2: sched → one ticker goroutine).
package frames

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// MetaRetention is the 90-day window for quarantined frame rows (§6.3) —
// the only meta the ring keeps beyond the 48 h stills window. Raw is never
// written and there are no session or health-event tables to prune.
const MetaRetention = 90 * 24 * time.Hour

// Prune runs one retention pass and reports how many frame rows were
// removed. File deletes happen first; a file that refuses to go keeps its
// row so the next pass retries (no orphans, no data loss).
func (s *Server) Prune(ctx context.Context) (int, error) {
	now := s.now().UTC()
	stale, err := s.st.StaleFrames(ctx, now, s.retainHours)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for _, sf := range stale {
		abs := filepath.Join(s.dataDir, sf.Path)
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("prune: file delete failed, will retry", "path", sf.Path, "err", err.Error())
			continue
		}
		ids = append(ids, sf.ID)
	}
	if err := s.st.DeleteFrames(ctx, ids); err != nil {
		return len(ids), err
	}
	if err := s.st.PruneMeta(ctx, now.Add(-MetaRetention)); err != nil {
		s.log.Warn("prune: meta retention failed", "err", err.Error())
	}
	s.dropOrphanTemps(now)
	if len(ids) > 0 {
		s.log.Info("prune: frames removed", "count", len(ids))
	}
	return len(ids), nil
}

// dropOrphanTemps removes temp files a crash may have left behind (a row is
// inserted before the rename, so nothing live can match these after an hour).
func (s *Server) dropOrphanTemps(now time.Time) {
	patterns := []string{
		filepath.Join(s.dataDir, "venues", "*", "stills", ".ingest-*.tmp"),
	}
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err == nil && now.Sub(fi.ModTime()) > time.Hour {
				os.Remove(m)
			}
		}
	}
}
