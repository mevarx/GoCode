package session

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"

	_ "modernc.org/sqlite"
)

// Reasoning must survive a round trip through the store: providers such as
// MiniMax and DeepSeek need the full assistant reasoning chain replayed into
// history on the next turn, so losing it on save or load breaks multi-turn
// tool calling after a restart.
// legacyDB is a bare SQLite handle used to build a pre-v0.5.2 database by hand.
type legacyDB struct {
	db *sql.DB
}

func newLegacyStore(t *testing.T, path string) *legacyDB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("failed to open legacy db: %v", err)
	}
	return &legacyDB{db: db}
}

func (l *legacyDB) exec(script string) error {
	_, err := l.db.Exec(script)
	return err
}

func (l *legacyDB) close() { _ = l.db.Close() }

func TestReasoningContentRoundTrips(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateSession("s1", "t", "minimax", "MiniMax-M2.5"); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	msg := provider.Message{
		Role:             "assistant",
		Content:          "the answer",
		ReasoningContent: "step one, step two",
		ToolCalls:        []provider.ToolCall{{ID: "c1", Name: "file_read", Args: []byte(`{"path":"a.go"}`)}},
	}
	if err := store.AppendMessage("s1", msg); err != nil {
		t.Fatalf("failed to append message: %v", err)
	}

	rec, err := store.GetSession("s1")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if len(rec.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(rec.Messages))
	}

	got := rec.Messages[0]
	if got.ReasoningContent != "step one, step two" {
		t.Errorf("reasoning content lost: got %q", got.ReasoningContent)
	}
	if got.Content != "the answer" {
		t.Errorf("content lost: got %q", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "c1" {
		t.Errorf("tool calls lost: got %+v", got.ToolCalls)
	}
}

// Reasoning must also survive SaveSession, which rewrites the whole transcript.
func TestReasoningContentSurvivesSaveSession(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	rec := &SessionRecord{
		ID:       "s2",
		Provider: "minimax",
		Model:    "MiniMax-M2.5",
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello", ReasoningContent: "thought chain"},
		},
	}
	if err := store.SaveSession(rec); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.GetSession("s2")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Messages[1].ReasoningContent != "thought chain" {
		t.Errorf("reasoning lost on SaveSession: got %q", loaded.Messages[1].ReasoningContent)
	}
}

// A database created before reasoning_content existed must be migrated in
// place, not fail to open. CREATE TABLE IF NOT EXISTS does not add columns to
// an existing table, so an explicit migration is required.
func TestExistingDatabaseIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a database with the pre-v0.5.2 schema by hand.
	legacy := newLegacyStore(t, path)
	if err := legacy.exec(`
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
			created_at DATETIME NOT NULL
		);
		INSERT INTO sessions (id, title, provider, model, created_at, updated_at)
		VALUES ('old', 'legacy', 'openai', 'gpt-4o', '2026-01-01', '2026-01-01');
		INSERT INTO messages (session_id, role, content, created_at)
		VALUES ('old', 'user', 'pre-existing message', '2026-01-01');
	`); err != nil {
		t.Fatalf("failed to build legacy schema: %v", err)
	}
	legacy.close()

	// Opening it with the current code must succeed and keep the old data.
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("opening a pre-existing database must not fail: %v", err)
	}
	defer store.Close()

	rec, err := store.GetSession("old")
	if err != nil {
		t.Fatalf("failed to load legacy session: %v", err)
	}
	if rec == nil {
		t.Fatal("expected the legacy session to still exist")
	}
	if len(rec.Messages) != 1 || rec.Messages[0].Content != "pre-existing message" {
		t.Fatalf("legacy messages were lost: %+v", rec.Messages)
	}

	// And the new column must now be usable.
	if err := store.AppendMessage("old", provider.Message{
		Role:             "assistant",
		Content:          "after migration",
		ReasoningContent: "fresh reasoning",
	}); err != nil {
		t.Fatalf("failed to write after migration: %v", err)
	}

	rec, err = store.GetSession("old")
	if err != nil {
		t.Fatal(err)
	}
	last := rec.Messages[len(rec.Messages)-1]
	if last.ReasoningContent != "fresh reasoning" {
		t.Errorf("reasoning not stored after migration: got %q", last.ReasoningContent)
	}
}
