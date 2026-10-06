package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	ok := map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "DEBUG": slog.LevelDebug,
		" warn ": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
	}
	for in, want := range ok {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("accepted unknown level")
	}
}

func TestInitHonoursAndChangesLevel(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	defer SetLevel("info")

	dir := t.TempDir()
	closer, err := Init(dir, "info", "text")
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	slog.Debug("hidden-debug-line")
	if err := SetLevel("debug"); err != nil {
		t.Fatal(err)
	}
	if Level() != "debug" {
		t.Fatalf("Level() = %q", Level())
	}
	slog.Debug("visible-debug-line")

	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 {
		t.Fatalf("log files: %v", files)
	}
	data, _ := os.ReadFile(files[0])
	got := string(data)
	if strings.Contains(got, "hidden-debug-line") {
		t.Error("debug line written while level was info")
	}
	if !strings.Contains(got, "visible-debug-line") {
		t.Error("debug line missing after enabling debug")
	}
	if !strings.Contains(got, `"msg":"visible-debug-line"`) {
		t.Error("file output is not JSON")
	}
}

func TestInitRejectsBadSettings(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)

	if _, err := Init(t.TempDir(), "loud", "json"); err == nil {
		t.Error("accepted bad level")
	}
	if _, err := Init(t.TempDir(), "info", "xml"); err == nil {
		t.Error("accepted bad format")
	}
}
