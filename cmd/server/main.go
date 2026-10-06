package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

	logCloser, err := logging.Init(config.LogDir)
	if err != nil {
		slog.Error("init logging failed", "error", err)
		os.Exit(1)
	}
	defer logCloser.Close()

	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	logging.StartCleanupScheduler(cleanupCtx, config.LogDir, config.LogRetentionDays)

	if err := storage.InitMinio(); err != nil {
		slog.Error("init minio client failed", "error", err)
		os.Exit(1)
	}

	db, err := store.Open(config.DBPath)
	if err != nil {
		slog.Error("open job store failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	queue.StartWorkers(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/minio-event", queue.HandleEvent(db))
	mux.HandleFunc("/api/stats", api.Stats(db))
	mux.HandleFunc("/api/jobs", api.Jobs(db))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("/", dashboard.Handler())

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
}
