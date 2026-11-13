package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

// isolateDataDir points the data directory (and therefore the log path) at a
// temporary tree so the test never touches the user's real logs.
func isolateDataDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
		return
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// A12: log lines can include request metadata, so the log file and its
// directory must be owner-only from creation.
func TestSetupLoggingCreatesOwnerOnlyLog(t *testing.T) {
	isolateDataDir(t)

	cleanup, err := setupLogging(false)
	if err != nil {
		t.Fatalf("setupLogging failed: %v", err)
	}
	defer cleanup()

	if runtime.GOOS == "windows" {
		return
	}

	info, err := os.Stat(config.LogFilePath())
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("expected log file mode 0600, got %04o", got)
	}

	dirInfo, err := os.Stat(config.LogDir())
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("expected log dir mode 0700, got %04o", got)
	}
}

// An older release created the log 0644 (or a user copied one in); opening it
// must not preserve that mode.
func TestSetupLoggingTightensExistingLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}
	isolateDataDir(t)

	logPath := config.LogFilePath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("old log line\n"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if err := os.Chmod(logPath, 0o644); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}

	cleanup, err := setupLogging(false)
	if err != nil {
		t.Fatalf("setupLogging failed: %v", err)
	}
	defer cleanup()

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("expected pre-existing log to be tightened to 0600, got %04o", got)
	}
}
