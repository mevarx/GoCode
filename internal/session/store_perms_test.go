package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// A4: WAL mode writes transcript data to -wal/-shm sidecars; they must be as
// private as the database itself, not whatever the process umask allows.
func TestSessionStoreWALSidecarPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}

	base := t.TempDir()
	// t.TempDir's numbered subdirectory is created with 0777&^umask (0755 on
	// CI); let NewStore create the dedicated session directory itself so the
	// assertion exercises the 0700 creation path.
	dir := filepath.Join(base, "sessions")
	dbPath := filepath.Join(dir, "sessions.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateSession("perm-1", "Permissions", "ollama", "llama"); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	if err := store.AppendMessage("perm-1", provider.Message{Role: "user", Content: "secret transcript"}); err != nil {
		t.Fatalf("failed to append message: %v", err)
	}

	paths := []string{dir, dbPath, dbPath + "-wal", dbPath + "-shm"}
	want := []os.FileMode{0o700, 0o600, 0o600, 0o600}
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := info.Mode().Perm(); perm != want[i] {
			t.Errorf("%s mode = %04o, want %04o", path, perm, want[i])
		}
	}
}
