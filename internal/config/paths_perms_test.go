package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A4: MkdirAll leaves an existing directory's mode alone, so EnsureDirs must
// actively tighten a sessions/config dir that was created with a loose mode.
func TestEnsurePrivateDirTightensExistingDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}

	dir := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	// Mkdir is masked by the umask; force the loose mode the helper must fix.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("failed to loosen dir: %v", err)
	}

	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("ensurePrivateDir failed: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", perm)
	}
}

// Missing directories must be created owner-only as well.
func TestEnsurePrivateDirCreatesDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}

	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("ensurePrivateDir failed: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", perm)
	}
}
