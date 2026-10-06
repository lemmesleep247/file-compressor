// Package logging writes structured logs to both stdout and a day-wise
// rotated file under a logs directory (logs/YYYY-MM-DD.log), and runs a
// background scheduler that deletes log files past a retention window.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const dateFormat = "2006-01-02"

// dailyWriter is an io.Writer that appends to logs/<today>.log, switching
// to a new file the first time it's written to on a new day.
type dailyWriter struct {
	mu   sync.Mutex
	dir  string
	day  string
	file *os.File
}

func newDailyWriter(dir string) (*dailyWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &dailyWriter{dir: dir}
	if err := w.rotate(time.Now()); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *dailyWriter) rotate(now time.Time) error {
	day := now.Format(dateFormat)
	if w.file != nil && w.day == day {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(w.dir, day+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if w.file != nil {
		w.file.Close()
	}
	w.file = f
	w.day = day
	return nil
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotate(time.Now()); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// Init points the process-wide slog default logger at both stdout and a
// daily-rotated file under dir. The returned io.Closer should be closed on
// shutdown to flush/close the current day's file.
func Init(dir string) (io.Closer, error) {
	w, err := newDailyWriter(dir)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(io.MultiWriter(os.Stdout, w), &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	return w, nil
}

// StartCleanupScheduler removes log files older than retentionDays, once
// immediately and then once every 24h until ctx is cancelled.
func StartCleanupScheduler(ctx context.Context, dir string, retentionDays int) {
	cleanupOnce(dir, retentionDays)

	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupOnce(dir, retentionDays)
			}
		}
	}()
}

func cleanupOnce(dir string, retentionDays int) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Error("log cleanup: read dir failed", "dir", dir, "error", err)
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		old := false
		if day, ok := dayFromFilename(e.Name()); ok {
			old = day.Before(cutoff)
		} else if info, err := e.Info(); err == nil {
			old = info.ModTime().Before(cutoff)
		}

		if !old {
			continue
		}

		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err != nil {
			slog.Error("log cleanup: remove failed", "file", path, "error", err)
		} else {
			slog.Info("log cleanup: removed old log", "file", path)
		}
	}
}

func dayFromFilename(name string) (time.Time, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	t, err := time.Parse(dateFormat, base)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
