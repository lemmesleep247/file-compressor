package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"file-compressor/internal/logging"
	"file-compressor/internal/queue"
	"file-compressor/internal/store"
)

// requireXHR rejects state-changing requests that don't carry a custom
// header. The dashboard authenticates with Basic auth, which browsers
// attach automatically to cross-site form posts; a custom header can't be
// set by a plain cross-site form and needs a CORS preflight (which we never
// grant) from fetch, so this blocks CSRF against the POST endpoints.
func requireXHR(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Requested-With") == "" {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return false
	}
	return true
}

// RetryJob handles POST /api/jobs/{id}/retry.
func RetryJob(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireXHR(w, r) {
			return
		}
		ok, err := db.Retry(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "no dead-letter job with that id", http.StatusNotFound)
			return
		}
		queue.Wake()
		w.WriteHeader(http.StatusNoContent)
	}
}

// Backfill handles GET (status) and POST (start) on /api/backfill.
// POST takes ?bucket=...&prefix=...
func Backfill(ctx context.Context, db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, queue.GetBackfillStatus())
		case http.MethodPost:
			if !requireXHR(w, r) {
				return
			}
			bucket := r.URL.Query().Get("bucket")
			if bucket == "" {
				http.Error(w, "bucket is required", http.StatusBadRequest)
				return
			}
			err := queue.StartBackfill(ctx, db, bucket, r.URL.Query().Get("prefix"))
			switch {
			case errors.Is(err, queue.ErrBackfillRunning):
				http.Error(w, err.Error(), http.StatusConflict)
			case errors.Is(err, queue.ErrBucketNotAllowed):
				http.Error(w, err.Error(), http.StatusBadRequest)
			case err != nil:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusAccepted)
				writeJSON(w, queue.GetBackfillStatus())
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// LogLevel handles GET (current level) and POST ?level=debug|info|warn|error
// on /api/loglevel, so debug logging can be switched on and off while the
// service is running. The change lasts until restart; LOG_LEVEL sets the
// startup value.
func LogLevel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			if !requireXHR(w, r) {
				return
			}
			if err := logging.SetLevel(r.URL.Query().Get("level")); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			slog.Info("log level changed", "level", logging.Level(), "remote_addr", r.RemoteAddr)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]string{"level": logging.Level()})
	}
}
