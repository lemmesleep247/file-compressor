package queue

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"file-compressor/internal/config"
	"file-compressor/internal/processor"
	"file-compressor/internal/storage"
	"file-compressor/internal/store"
)

// ErrBackfillRunning is returned when a backfill is already in progress.
var ErrBackfillRunning = errors.New("a backfill is already running")

// ErrBucketNotAllowed is returned for buckets excluded by configuration.
var ErrBucketNotAllowed = errors.New("bucket is not allowed")

// BackfillStatus describes the current or most recent backfill.
type BackfillStatus struct {
	Running  bool   `json:"running"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
	Scanned  int64  `json:"scanned"`
	Enqueued int64  `json:"enqueued"`
	Error    string `json:"error,omitempty"`
}

var (
	backfillMu     sync.Mutex
	backfillStatus BackfillStatus
)

// GetBackfillStatus returns a snapshot of the latest backfill.
func GetBackfillStatus() BackfillStatus {
	backfillMu.Lock()
	defer backfillMu.Unlock()
	return backfillStatus
}

func updateBackfill(f func(*BackfillStatus)) {
	backfillMu.Lock()
	defer backfillMu.Unlock()
	f(&backfillStatus)
}

// StartBackfill lists bucket/prefix in the background and enqueues every
// object that could be processed, covering uploads that predate the
// webhook. Only one backfill runs at a time. Objects already compressed
// in place are still enqueued; their jobs finish quickly because the
// stored hash shows there is nothing left to do.
func StartBackfill(ctx context.Context, db *store.Store, bucket, prefix string) error {
	if !BucketAllowed(bucket) {
		return ErrBucketNotAllowed
	}

	backfillMu.Lock()
	if backfillStatus.Running {
		backfillMu.Unlock()
		return ErrBackfillRunning
	}
	backfillStatus = BackfillStatus{Running: true, Bucket: bucket, Prefix: prefix}
	backfillMu.Unlock()

	go func() {
		err := runBackfill(ctx, db, bucket, prefix)
		updateBackfill(func(s *BackfillStatus) {
			s.Running = false
			if err != nil {
				s.Error = err.Error()
			}
		})
		st := GetBackfillStatus()
		slog.Info("backfill finished", "bucket", bucket, "prefix", prefix, "scanned", st.Scanned, "enqueued", st.Enqueued, "error", st.Error)
	}()
	return nil
}

func runBackfill(ctx context.Context, db *store.Store, bucket, prefix string) error {
	for obj := range storage.List(ctx, bucket, prefix) {
		if obj.Err != nil {
			return obj.Err
		}
		updateBackfill(func(s *BackfillStatus) { s.Scanned++ })

		// Same cheap pre-filters the worker applies, so we don't queue
		// jobs that would be no-ops.
		if strings.HasSuffix(obj.Key, processor.CompressedSuffix) ||
			strings.HasSuffix(obj.Key, "/") ||
			obj.Size < int64(config.MinSizeKB)*1024 ||
			(config.MaxFileSizeMB > 0 && obj.Size > int64(config.MaxFileSizeMB)*1024*1024) {
			continue
		}

		_, created, err := db.Enqueue(bucket, obj.Key)
		if err != nil {
			return err
		}
		if created {
			updateBackfill(func(s *BackfillStatus) { s.Enqueued++ })
			notify()
		}
	}
	return ctx.Err()
}
