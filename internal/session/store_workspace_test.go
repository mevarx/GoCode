package session

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"

	_ "modernc.org/sqlite"
)

// A database written before sessions tracked a workspace must gain the column
// in place, and its rows must load as workspace-less without losing messages.
func TestLegacyDatabaseGainsWorkspaceColumn(t *testing.T) {
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
		INSERT INTO sessions (id, title, provider, model, created_at, updated_at)
		VALUES ('old', 'legacy', 'openai', 'gpt-4o', '2026-01-01', '2026-01-01');
		INSERT INTO messages (session_id, role, content, created_at)
		VALUES ('old', 'user', 'pre-existing message', '2026-01-01');
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

	exists, err := store.columnExists("sessions", "workspace")
	if err != nil {
		t.Fatalf("failed to inspect sessions schema: %v", err)
	}
	if !exists {
		t.Fatal("expected sessions.workspace to be added by the migration")
	}

	rec, err := store.GetSession("old")
	if err != nil {
		t.Fatalf("failed to load legacy session: %v", err)
	}
	if rec == nil {
		t.Fatal("expected the legacy session to still exist")
	}
	if rec.Workspace != "" {
		t.Errorf("legacy session should have an empty workspace, got %q", rec.Workspace)
	}
	if len(rec.Messages) != 1 || rec.Messages[0].Content != "pre-existing message" {
		t.Fatalf("legacy messages lost: %+v", rec.Messages)
	}

	// A workspace-less row must never be picked up by workspace auto-resume.
	last, err := store.GetLastSessionForWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("failed to query by workspace: %v", err)
	}
	if last != nil {
		t.Errorf("workspace-less session must not auto-resume, got %q", last.ID)
	}
}

// Auto-resume must stay inside the workspace that recorded the transcript.
func TestGetLastSessionForWorkspaceScopesResume(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	if _, err := store.CreateSessionWithWorkspace("sess-a", "Repo A", "openai", "gpt-4o", workspaceA); err != nil {
		t.Fatalf("failed to create session A: %v", err)
	}
	if err := store.AppendMessage("sess-a", provider.Message{Role: "user", Content: "secret from repo A"}); err != nil {
		t.Fatalf("failed to append message: %v", err)
	}

	got, err := store.GetLastSessionForWorkspace(workspaceA)
	if err != nil {
		t.Fatalf("failed to query workspace A: %v", err)
	}
	if got == nil || got.ID != "sess-a" {
		t.Fatalf("expected sess-a for workspace A, got %+v", got)
	}
	if got.Workspace != normalizeWorkspace(workspaceA) {
		t.Errorf("workspace not persisted: got %q", got.Workspace)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "secret from repo A" {
		t.Errorf("messages not loaded: %+v", got.Messages)
	}

	other, err := store.GetLastSessionForWorkspace(workspaceB)
	if err != nil {
		t.Fatalf("failed to query workspace B: %v", err)
	}
	if other != nil {
		t.Errorf("workspace B must not resume repo A's transcript, got %q", other.ID)
	}

	if _, err := store.CreateSessionWithWorkspace("sess-b", "Repo B", "openai", "gpt-4o", workspaceB); err != nil {
		t.Fatalf("failed to create session B: %v", err)
	}

	gotB, err := store.GetLastSessionForWorkspace(workspaceB)
	if err != nil {
		t.Fatalf("failed to query workspace B: %v", err)
	}
	if gotB == nil || gotB.ID != "sess-b" {
		t.Fatalf("expected sess-b for workspace B, got %+v", gotB)
	}

	stillA, err := store.GetLastSessionForWorkspace(workspaceA)
	if err != nil {
		t.Fatalf("failed to query workspace A again: %v", err)
	}
	if stillA == nil || stillA.ID != "sess-a" {
		t.Fatalf("workspace A must keep its own session, got %+v", stillA)
	}

	// Global selection and listing stay unscoped.
	global, err := store.GetLastSession()
	if err != nil {
		t.Fatalf("failed to query last session: %v", err)
	}
	if global == nil {
		t.Fatal("expected a global last session")
	}
	summaries, err := store.ListSessions(10)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(summaries) != 2 {
		t.Errorf("listing must stay global, got %d summaries", len(summaries))
	}

	// An empty workspace identifies no directory and never matches.
	empty, err := store.GetLastSessionForWorkspace("")
	if err != nil {
		t.Fatalf("failed to query empty workspace: %v", err)
	}
	if empty != nil {
		t.Errorf("empty workspace must not auto-resume, got %q", empty.ID)
	}
}

// A relative or uncleaned path must match the stored absolute workspace.
func TestGetLastSessionForWorkspaceNormalizesPaths(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	workspace := t.TempDir()
	if _, err := store.CreateSessionWithWorkspace("sess-norm", "", "openai", "gpt-4o", workspace+string(filepath.Separator)); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	got, err := store.GetLastSessionForWorkspace(filepath.Join(workspace, "."))
	if err != nil {
		t.Fatalf("failed to query normalized workspace: %v", err)
	}
	if got == nil || got.ID != "sess-norm" {
		t.Fatalf("expected normalized workspace to match, got %+v", got)
	}
}

// /clear saves a record without workspace knowledge; that must not erase the
// session's workspace binding.
func TestSaveSessionWithoutWorkspaceKeepsBinding(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	workspace := t.TempDir()
	rec, err := store.CreateSessionWithWorkspace("sess-keep", "Keep", "openai", "gpt-4o", workspace)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	rec.Workspace = ""
	rec.Messages = nil
	if err := store.SaveSession(rec); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.GetSession("sess-keep")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected session to exist")
	}
	if loaded.Workspace != normalizeWorkspace(workspace) {
		t.Errorf("workspace binding lost on SaveSession: got %q", loaded.Workspace)
	}
}
