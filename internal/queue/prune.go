package queue

import (
	"context"
	"log/slog"
	"time"

	"file-compressor/internal/store"
)

// StartPruner deletes finished job rows older than retentionDays, once at
// startup and then daily, until ctx is cancelled. retentionDays <= 0
// disables pruning. Note the dashboard's totals are computed from retained
// rows, so they cover the retention window.
func StartPruner(ctx context.Context, db *store.Store, retentionDays int) {
	if retentionDays <= 0 {
		return
	}

	run := func() {
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		n, err := db.Prune(cutoff)
		if err != nil {
			slog.Error("job prune failed", "error", err)
		} else if n > 0 {
			slog.Info("pruned old jobs", "count", n, "older_than_days", retentionDays)
		}
	}

	go func() {
		run()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
