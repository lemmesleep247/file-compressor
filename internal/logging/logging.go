// Package logging writes structured logs to both stdout and a day-wise
// rotated file under a logs directory (logs/YYYY-MM-DD.log), and runs a
// background scheduler that deletes log files past a retention window.
package logging

import (
	"context"
	"fmt"
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

// level is the process-wide minimum log level. It is a LevelVar so it can
// be changed at runtime (see SetLevel) without a restart.
var level slog.LevelVar

// ParseLevel converts debug|info|warn|error (case-insensitive; empty means
// info) to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// SetLevel changes the minimum log level for the running process.
func SetLevel(s string) error {
	l, err := ParseLevel(s)
	if err != nil {
		return err
	}
	level.Set(l)
	return nil
}

// Level returns the current minimum level as debug|info|warn|error.
func Level() string {
	return strings.ToLower(level.Level().String())
}

// teeHandler sends every record to each handler that wants it.
type teeHandler []slog.Handler

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range t {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(name)
	}
	return out
}

// Init points the process-wide slog default logger at stdout and a
// daily-rotated JSON file under dir. lvl is the initial level
// (debug|info|warn|error) and format is the stdout encoding: "json" or
// "text" (human-friendly for a terminal). The file is always JSON so it
// stays machine-parseable. The returned io.Closer should be closed on
// shutdown to flush/close the current day's file.
func Init(dir, lvl, format string) (io.Closer, error) {
	if err := SetLevel(lvl); err != nil {
		return nil, err
	}

	w, err := newDailyWriter(dir)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: &level}
	var console slog.Handler
	switch strings.ToLower(format) {
	case "", "json":
		console = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		console = slog.NewTextHandler(os.Stdout, opts)
	default:
		w.Close()
		return nil, fmt.Errorf("unknown log format %q (want json or text)", format)
	}

	slog.SetDefault(slog.New(teeHandler{console, slog.NewJSONHandler(w, opts)}))
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
