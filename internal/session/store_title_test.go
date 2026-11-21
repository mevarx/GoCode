package session

import (
	"path/filepath"
	"testing"
)

// UpdateTitle renames a session through the same truncation as creation.
func TestUpdateTitleRenamesSession(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateSession("id1", "old title", "ollama", "llama3"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.UpdateTitle("id1", "new title"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	rec, err := store.GetSession("id1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Title != "new title" {
		t.Fatalf("title = %q, want %q", rec.Title, "new title")
	}
}
