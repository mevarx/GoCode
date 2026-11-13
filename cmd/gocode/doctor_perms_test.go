package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A4: doctor must warn when a session transcript path is readable by other users.
func TestSessionStorageWarningFlagsGroupOtherReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}

	path := filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(path, []byte("transcript"), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	// WriteFile is masked by the umask; force the loose mode the check must catch.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("failed to loosen file: %v", err)
	}

	warning := sessionStorageWarning(path)
	if warning == "" {
		t.Fatal("expected a warning for a 0644 session file")
	}
	if !strings.Contains(warning, "mode 0644") {
		t.Errorf("warning should name the offending mode, got %q", warning)
	}
	if !strings.Contains(warning, "expected 0600/0700") {
		t.Errorf("warning should name the expected mode, got %q", warning)
	}
}

func TestSessionStorageWarningAcceptsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}

	path := filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(path, []byte("transcript"), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("failed to set mode: %v", err)
	}

	if warning := sessionStorageWarning(path); warning != "" {
		t.Errorf("expected no warning for a 0600 file, got %q", warning)
	}
}

// Sidecars may not exist yet on a fresh install; that is not a finding.
func TestSessionStorageWarningIgnoresMissingPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}

	path := filepath.Join(t.TempDir(), "sessions.db-wal")
	if warning := sessionStorageWarning(path); warning != "" {
		t.Errorf("expected no warning for a missing path, got %q", warning)
	}
}
