package session

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestScratchpadRoundTrip(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	rec, err := store.CreateSessionWithWorkspace("s1", "title", "prov", "model", t.TempDir())
	if err != nil {
		t.Fatalf("CreateSessionWithWorkspace: %v", err)
	}
	if rec.Scratchpad != "" {
		t.Fatalf("new session scratchpad should be empty, got %q", rec.Scratchpad)
	}

	if err := store.UpdateScratchpad("s1", "Goal: finish C1"); err != nil {
		t.Fatalf("UpdateScratchpad: %v", err)
	}
	got, err := store.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Scratchpad != "Goal: finish C1" {
		t.Fatalf("scratchpad = %q, want round-trip value", got.Scratchpad)
	}
}

// A caller that does not know about the scratchpad (for example /clear) must
// not erase the stored working state.
func TestSaveSessionEmptyScratchpadDoesNotClobber(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateSessionWithWorkspace("s1", "title", "prov", "model", ""); err != nil {
		t.Fatalf("CreateSessionWithWorkspace: %v", err)
	}
	if err := store.UpdateScratchpad("s1", "keep me"); err != nil {
		t.Fatalf("UpdateScratchpad: %v", err)
	}

	rec, err := store.GetSession("s1")
	if err != nil || rec == nil {
		t.Fatalf("GetSession: rec=%v err=%v", rec, err)
	}
	rec.Messages = nil // simulate /clear's record
	if err := store.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := store.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession after save: %v", err)
	}
	if got.Scratchpad != "keep me" {
		t.Fatalf("empty incoming scratchpad clobbered stored value: %q", got.Scratchpad)
	}

	// A non-empty incoming value is authoritative.
	rec.Scratchpad = "updated"
	if err := store.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession update: %v", err)
	}
	got, err = store.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}
	if got.Scratchpad != "updated" {
		t.Fatalf("non-empty scratchpad not saved: %q", got.Scratchpad)
	}
}

// A database written before sessions tracked a scratchpad must gain the column
// in place, with existing rows reading back as empty.
func TestLegacyDatabaseGainsScratchpadColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("failed to open legacy db: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			workspace TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_calls TEXT,
			tool_call_id TEXT,
			reasoning_content TEXT,
			created_at DATETIME NOT NULL
		);
		INSERT INTO sessions (id, title, provider, model, workspace, created_at, updated_at)
		VALUES ('old', 'legacy', 'openai', 'gpt-4o', '', '2026-01-01', '2026-01-01');
	`); err != nil {
		db.Close()
		t.Fatalf("failed to build legacy schema: %v", err)
	}
	db.Close()

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("opening a pre-existing database must not fail: %v", err)
	}
	defer store.Close()

	exists, err := store.columnExists("sessions", "scratchpad")
	if err != nil {
		t.Fatalf("failed to inspect sessions schema: %v", err)
	}
	if !exists {
		t.Fatal("expected sessions.scratchpad to be added by the migration")
	}

	rec, err := store.GetSession("old")
	if err != nil {
		t.Fatalf("failed to load legacy session: %v", err)
	}
	if rec == nil {
		t.Fatal("expected the legacy session to still exist")
	}
	if rec.Scratchpad != "" {
		t.Fatalf("legacy session scratchpad = %q, want empty", rec.Scratchpad)
	}

	if err := store.UpdateScratchpad("old", "new state"); err != nil {
		t.Fatalf("UpdateScratchpad on legacy row: %v", err)
	}
	rec, err = store.GetSession("old")
	if err != nil || rec == nil {
		t.Fatalf("GetSession after update: rec=%v err=%v", rec, err)
	}
	if rec.Scratchpad != "new state" {
		t.Fatalf("scratchpad not persisted on migrated row: %q", rec.Scratchpad)
	}
}
