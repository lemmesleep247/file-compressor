package queue

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"file-compressor/internal/config"
	"file-compressor/internal/processor"
	"file-compressor/internal/store"
)

// wake lets producers (HandleEvent, retry backoff) nudge idle workers
// instead of relying purely on the poll ticker below.
var wake = make(chan struct{}, 1)

// Wake nudges idle workers, e.g. after a job was revived from outside the
// queue package.
func Wake() { notify() }

func notify() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// StartWorkers requeues any job stranded in "processing" by a previous
// crash, then launches the worker pool. Workers pull jobs from the store
// rather than a channel, so queue state survives restarts and isn't bounded
// by an in-memory channel's capacity.
//
// Cancelling ctx stops workers from claiming new jobs; a job already
// running is allowed to finish. Wait on the returned group to drain them.
func StartWorkers(ctx context.Context, db *store.Store) *sync.WaitGroup {
	if err := db.RequeueStuckProcessing(); err != nil {
		slog.Error("requeue stuck jobs failed", "error", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < config.WorkerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			worker(ctx, id, db)
		}(i)
	}
	return &wg
}

func worker(ctx context.Context, id int, db *store.Store) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// idle blocks until there may be work again; false means shut down.
	idle := func(extra <-chan struct{}) bool {
		select {
		case <-ctx.Done():
			return false
		case <-extra:
		case <-ticker.C:
		}
		return true
	}

	for ctx.Err() == nil {
		job, err := db.ClaimNext()
		if err != nil {
			slog.Error("claim job failed", "worker", id, "error", err)
			if !idle(nil) {
				return
			}
			continue
		}
		if job == nil {
			if !idle(wake) {
				return
			}
			continue
		}

		runJob(db, job, id)
	}
}

func runJob(db *store.Store, job *store.Job, workerID int) {
	log := slog.With("job", job.ID, "worker", workerID, "bucket", job.Bucket, "object", job.Object)
	log.Debug("job started", "attempt", job.Retries+1, "queued_for_ms", time.Now().UnixMilli()-job.CreatedAt)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.JobTimeoutSeconds)*time.Second)
	defer cancel()

	origSize, finalSize, err := processor.ProcessObject(ctx, job.Bucket, job.Object)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		permanent := processor.IsPermanent(err)
		dead, ferr := db.Fail(job.ID, err, config.MaxRetries, permanent)
		if ferr != nil {
			log.Error("record job failure failed", "error", ferr)
		}
		if dead {
			log.Warn("job moved to dead letter", "attempts", job.Retries+1, "permanent", permanent, "duration_ms", elapsed, "error", err)
			// Fresh context: the job's own may be the thing that expired.
			dlqCtx, dlqCancel := context.WithTimeout(context.Background(), 30*time.Second)
			dlqErr := processor.SendToDLQ(dlqCtx, job.Bucket, job.Object, err)
			dlqCancel()
			if dlqErr != nil {
				log.Error("send to DLQ failed", "error", dlqErr)
			} else {
				log.Debug("dead-letter record written", "dlq_bucket", config.DLQBucket)
			}
		} else {
			log.Warn("job failed, will retry", "attempt", job.Retries+1, "max_retries", config.MaxRetries, "duration_ms", elapsed, "error", err)
		}
		return
	}

	if err := db.Complete(job.ID, origSize, finalSize); err != nil {
		log.Error("record job completion failed", "error", err)
	}

	if finalSize < origSize {
		log.Info("compressed object",
			"original_bytes", origSize,
			"compressed_bytes", finalSize,
			"saved_pct", float64(origSize-finalSize)*100/float64(origSize),
			"duration_ms", elapsed,
		)
	} else {
		log.Debug("job finished with no size change", "size_bytes", origSize, "duration_ms", elapsed)
	}
}

// validWebhookToken accepts config.WebhookToken via either the
// Authorization: Bearer header MinIO sends when configured with
// notify_webhook's auth_token, or a plain X-Webhook-Token header for
// clients that set it directly (e.g. curl, a fronting proxy).
func validWebhookToken(r *http.Request) bool {
	token := r.Header.Get("X-Webhook-Token")
	if auth := r.Header.Get("Authorization"); token == "" && strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(config.WebhookToken)) == 1
}

// HandleEvent accepts a MinIO bucket-notification webhook payload, persists
// one job per record, and returns immediately — processing happens
// asynchronously in the worker pool.
func HandleEvent(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if config.WebhookToken != "" && !validWebhookToken(r) {
			slog.Warn("webhook rejected: bad or missing token", "remote_addr", r.RemoteAddr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB cap; event payloads are small JSON

		targets, err := ParseEvent(r.Body)
		if err != nil {
			slog.Warn("webhook rejected: unparseable payload", "remote_addr", r.RemoteAddr, "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var queued, duplicates, failed int
		for _, t := range targets {
			_, created, err := db.Enqueue(t.Bucket, t.Object)
			switch {
			case err != nil:
				failed++
				slog.Error("enqueue failed", "bucket", t.Bucket, "object", t.Object, "error", err)
			case created:
				queued++
				notify()
				slog.Debug("job queued", "bucket", t.Bucket, "object", t.Object)
			default:
				duplicates++
				slog.Debug("already queued, ignoring duplicate event", "bucket", t.Bucket, "object", t.Object)
			}
		}
		if len(targets) > 0 {
			slog.Info("webhook received", "objects", len(targets), "queued", queued, "duplicates", duplicates, "enqueue_errors", failed)
		}

		w.WriteHeader(http.StatusOK)
	}
}

// Target is one object a webhook asked us to process.
type Target struct {
	Bucket string
	Object string
}

// ParseEvent decodes a MinIO bucket-notification payload into the objects
// worth processing. It decodes the percent-encoded object key MinIO sends,
// and drops events that aren't object creations, buckets that aren't
// allowed (or are the dead-letter bucket, which would feed itself), and
// the .zst siblings this service writes.
func ParseEvent(body io.Reader) ([]Target, error) {
	var payload struct {
		Records []struct {
			EventName string `json:"eventName"`
			S3        struct {
				Bucket struct{ Name string }
				Object struct{ Key string }
			}
		}
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, err
	}

	var out []Target
	for _, rec := range payload.Records {
		if !strings.HasPrefix(rec.EventName, "s3:ObjectCreated:") {
			slog.Debug("event ignored: not an object creation", "event", rec.EventName, "bucket", rec.S3.Bucket.Name)
			continue
		}

		bucket := rec.S3.Bucket.Name
		key, err := url.QueryUnescape(rec.S3.Object.Key)
		if err != nil {
			slog.Warn("ignoring event with undecodable key", "bucket", bucket, "key", rec.S3.Object.Key)
			continue
		}

		switch {
		case bucket == "" || key == "":
			slog.Debug("event ignored: empty bucket or key")
			continue
		case !BucketAllowed(bucket):
			slog.Debug("event ignored: bucket not allowed", "bucket", bucket, "object", key)
			continue
		case strings.HasSuffix(key, processor.CompressedSuffix):
			slog.Debug("event ignored: compressed sibling", "bucket", bucket, "object", key)
			continue
		}
		out = append(out, Target{Bucket: bucket, Object: key})
	}
	return out, nil
}

// BucketAllowed reports whether events/backfills for bucket may be processed.
func BucketAllowed(bucket string) bool {
	// Never process our own destinations, or their events would feed back.
	if bucket == config.DLQBucket || (config.OutputBucket != "" && bucket == config.OutputBucket) {
		return false
	}
	return len(config.AllowedBuckets) == 0 || slices.Contains(config.AllowedBuckets, bucket)
}
