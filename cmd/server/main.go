package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"file-compressor/internal/api"
	"file-compressor/internal/api/dashboard"
	"file-compressor/internal/config"
	"file-compressor/internal/logging"
	"file-compressor/internal/queue"
	"file-compressor/internal/storage"
	"file-compressor/internal/store"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	config.Load()
	if err := config.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logCloser, err := logging.Init(config.LogDir, config.LogLevel, config.LogFormat)
	if err != nil {
		slog.Error("init logging failed", "error", err)
		os.Exit(1)
	}
	defer logCloser.Close()

	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	slog.Info("starting",
		"workers", config.WorkerCount,
		"max_retries", config.MaxRetries,
		"mode", processingMode(),
		"output_bucket", config.OutputBucket,
		"allowed_buckets", config.AllowedBuckets,
		"dlq_bucket", config.DLQBucket,
		"webp", config.EnableWebP,
		"image_quality", config.ImageQuality,
		"max_image_dimension", config.MaxImageDimension,
		"generic_compression", config.EnableGenericCompression,
		"job_timeout_s", config.JobTimeoutSeconds,
		"log_level", logging.Level(),
		"insecure", config.AllowInsecure,
	)
	logging.StartCleanupScheduler(cleanupCtx, config.LogDir, config.LogRetentionDays)

	if !config.AllowInsecure {
		if config.WebhookToken == "" || config.DashboardToken == "" {
			slog.Error("WEBHOOK_TOKEN and DASHBOARD_TOKEN must both be set (or set ALLOW_INSECURE=true for local dev)")
			os.Exit(1)
		}
	}

	if err := storage.InitMinio(); err != nil {
		slog.Error("init minio client failed", "error", err)
		os.Exit(1)
	}

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
	err = storage.Ping(pingCtx)
	cancelPing()
	if err != nil {
		slog.Error("cannot reach MinIO with the configured endpoint/keys", "error", err)
		os.Exit(1)
	}

	if config.OutputBucket != "" {
		if config.OutputBucket == config.DLQBucket || slices.Contains(config.AllowedBuckets, config.OutputBucket) {
			slog.Error("OUTPUT_BUCKET must differ from DLQ_BUCKET and not be in ALLOWED_BUCKETS", "output_bucket", config.OutputBucket)
			os.Exit(1)
		}
		ensureCtx, cancelEnsure := context.WithTimeout(context.Background(), 10*time.Second)
		err := storage.EnsureBucket(ensureCtx, config.OutputBucket)
		cancelEnsure()
		if err != nil {
			slog.Error("cannot create/access OUTPUT_BUCKET", "bucket", config.OutputBucket, "error", err)
			os.Exit(1)
		}
	}

	db, err := store.Open(config.DBPath)
	if err != nil {
		slog.Error("open job store failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	workers := queue.StartWorkers(workerCtx, db)
	queue.StartPruner(workerCtx, db, config.JobRetentionDays)

	mux := http.NewServeMux()
	mux.HandleFunc("/minio-event", queue.HandleEvent(db))
	mux.Handle("/api/stats", api.RequireToken(config.DashboardToken, api.Stats(db)))
	mux.Handle("/api/jobs", api.RequireToken(config.DashboardToken, api.Jobs(db)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("POST /api/jobs/{id}/retry", api.RequireToken(config.DashboardToken, api.RetryJob(db)))
	mux.Handle("/api/loglevel", api.RequireToken(config.DashboardToken, api.LogLevel()))
	mux.Handle("/api/backfill", api.RequireToken(config.DashboardToken, api.Backfill(workerCtx, db)))
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := db.Ping(); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := storage.Ping(ctx); err != nil {
			http.Error(w, "minio unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.Handle("/", api.RequireToken(config.DashboardToken, dashboard.Handler()))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	addr := ":" + port

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		serveErr <- srv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-stop:
		slog.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
		}
	}

	// Stop claiming new jobs and give in-flight ones a bounded time to
	// finish. Anything cut off is requeued by RequeueStuckProcessing on the
	// next start.
	stopWorkers()
	drained := make(chan struct{})
	go func() { workers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(30 * time.Second):
		slog.Warn("workers did not finish in time; unfinished jobs will be requeued on restart")
	}
}

func processingMode() string {
	if config.OutputBucket != "" {
		return "copy"
	}
	return "in_place"
}
