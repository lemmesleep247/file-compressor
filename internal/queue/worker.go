package queue

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"file-compressor/internal/config"
	"file-compressor/internal/processor"
	"file-compressor/internal/store"
)

// wake lets producers (HandleEvent, retry backoff) nudge idle workers
// instead of relying purely on the poll ticker below.
var wake = make(chan struct{}, 1)

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
func StartWorkers(db *store.Store) {
	if err := db.RequeueStuckProcessing(); err != nil {
		slog.Error("requeue stuck jobs failed", "error", err)
	}

	for i := 0; i < config.WorkerCount; i++ {
		go worker(i, db)
	}
}

func worker(id int, db *store.Store) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		job, err := db.ClaimNext()
		if err != nil {
			slog.Error("claim job failed", "worker", id, "error", err)
			<-ticker.C
			continue
		}
		if job == nil {
			select {
			case <-wake:
			case <-ticker.C:
			}
			continue
		}

		runJob(db, job)
	}
}

func runJob(db *store.Store, job *store.Job) {
	origSize, finalSize, err := processor.ProcessObject(job.Bucket, job.Object)
	if err != nil {
		dead, ferr := db.Fail(job.ID, err, config.MaxRetries)
		if ferr != nil {
			slog.Error("record job failure failed", "job", job.ID, "error", ferr)
		}
		if dead {
			slog.Warn("job moved to dead letter", "bucket", job.Bucket, "object", job.Object, "retries", job.Retries+1, "error", err)
			if dlqErr := processor.SendToDLQ(job.Bucket, job.Object, err); dlqErr != nil {
				slog.Error("send to DLQ failed", "job", job.ID, "error", dlqErr)
			}
		} else {
			slog.Warn("job failed, will retry", "bucket", job.Bucket, "object", job.Object, "retry", job.Retries+1, "error", err)
		}
		return
	}

	if err := db.Complete(job.ID, origSize, finalSize); err != nil {
		slog.Error("record job completion failed", "job", job.ID, "error", err)
	}

	if finalSize < origSize {
		slog.Info("compressed object", "bucket", job.Bucket, "object", job.Object, "original_bytes", origSize, "compressed_bytes", finalSize)
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
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB cap; event payloads are small JSON

		var payload struct {
			Records []struct {
				S3 struct {
					Bucket struct{ Name string }
					Object struct{ Key string }
				}
			}
		}

		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		for _, rec := range payload.Records {
			if _, created, err := db.Enqueue(rec.S3.Bucket.Name, rec.S3.Object.Key); err != nil {
				slog.Error("enqueue failed", "bucket", rec.S3.Bucket.Name, "object", rec.S3.Object.Key, "error", err)
			} else if created {
				notify()
			}
		}

		w.WriteHeader(http.StatusOK)
	}
}
