package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mevarx/GoCode/internal/provider"
)

func TestSessionStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_sessions.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sess, err := store.CreateSession("test-1", "First Session", "ollama", "codellama")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	if sess.ID != "test-1" || sess.Provider != "ollama" || sess.Model != "codellama" {
		t.Errorf("unexpected session fields: %+v", sess)
	}

	msg1 := provider.Message{Role: "user", Content: "Hello world"}
	if err := store.AppendMessage(sess.ID, msg1); err != nil {
		t.Fatalf("failed to append message: %v", err)
	}

	msg2 := provider.Message{
		Role:    "assistant",
		Content: "Hi there!",
		ToolCalls: []provider.ToolCall{
			{ID: "call-1", Name: "file_read", Args: json.RawMessage(`{"path":"main.go"}`)},
		},
	}
	if err := store.AppendMessage(sess.ID, msg2); err != nil {
		t.Fatalf("failed to append assistant message: %v", err)
	}

	loaded, err := store.GetSession("test-1")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected session not nil")
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Content != "Hello world" {
		t.Errorf("msg 0 mismatch: %s", loaded.Messages[0].Content)
	}
	if len(loaded.Messages[1].ToolCalls) != 1 || loaded.Messages[1].ToolCalls[0].Name != "file_read" {
		t.Errorf("tool call mismatch: %+v", loaded.Messages[1].ToolCalls)
	}

	last, err := store.GetLastSession()
	if err != nil {
		t.Fatalf("failed to get last session: %v", err)
	}
	if last == nil || last.ID != "test-1" {
		t.Errorf("expected last session to be test-1, got %+v", last)
	}

	sess2, err := store.CreateSession("test-2", "Second Session", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("failed to create session 2: %v", err)
	}
	_ = store.AppendMessage(sess2.ID, provider.Message{Role: "user", Content: "Second"})

	last2, err := store.GetLastSession()
	if err != nil {
		t.Fatalf("failed to get last session: %v", err)
	}
	if last2.ID != "test-2" {
		t.Errorf("expected last session test-2, got %s", last2.ID)
	}

	summaries, err := store.ListSessions(10)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	if err := store.DeleteSession("test-1"); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}
	deleted, err := store.GetSession("test-1")
	if err != nil {
		t.Fatalf("unexpected error getting deleted session: %v", err)
	}
	if deleted != nil {
		t.Errorf("expected deleted session to be nil, got %+v", deleted)
	}
}

// MEDIUM-33: transcripts may hold file contents, so DB must be 0600 and its dir 0700; skipped on Windows (ACLs apply).
func TestSessionStoreFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go mode bits are ignored on Windows; ACL inheritance applies instead")
	}
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "sub", "sessions.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("database file mode = %04o, want 0600", perm)
	}

	dirFi, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat dir failed: %v", err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("session dir mode = %04o, want 0700", perm)
	}
}

// MEDIUM-26/MEDIUM-27: busy_timeout, foreign_keys, WAL, and single-connection pool must hold (pragmas are per-connection).
func TestSessionStorePragmasAndPool(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}

	query := func(pragma string) string {
		var v string
		if err := store.db.QueryRow("PRAGMA " + pragma).Scan(&v); err != nil {
			t.Fatalf("PRAGMA %s failed: %v", pragma, err)
		}
		return v
	}
	if got := query("busy_timeout"); got != "5000" {
		t.Errorf("busy_timeout = %q, want 5000", got)
	}
	if got := query("foreign_keys"); got != "1" {
		t.Errorf("foreign_keys = %q, want 1", got)
	}
	if got := query("journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %q, want wal", got)
	}
}

// B4: the auto title is truncated by runes, so multibyte first messages never yield split UTF-8.
func TestAppendMessageTitleTruncatesByRunes(t *testing.T) {
	cjk50 := strings.Repeat("汉", 50)
	emojiWithTail := strings.Repeat("a", 36) + "😀" + strings.Repeat("b", 20)
	ascii50 := strings.Repeat("a", 50)
	cjk40 := strings.Repeat("字", 40)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "50 CJK runes", input: cjk50, want: string([]rune(cjk50)[:37]) + "..."},
		{name: "emoji at truncation boundary", input: emojiWithTail, want: strings.Repeat("a", 36) + "😀" + "..."},
		{name: "long ASCII unchanged", input: ascii50, want: ascii50[:37] + "..."},
		{name: "exactly 40 runes kept", input: cjk40, want: cjk40},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
			if err != nil {
				t.Fatalf("failed to create store: %v", err)
			}
			defer store.Close()

			// Empty title makes CreateSession use the "Session sess_..." placeholder that triggers the title update.
			if _, err := store.CreateSession("sess_title", "", "ollama", "codellama"); err != nil {
				t.Fatalf("failed to create session: %v", err)
			}
			if err := store.AppendMessage("sess_title", provider.Message{Role: "user", Content: tc.input}); err != nil {
				t.Fatalf("failed to append message: %v", err)
			}

			rec, err := store.GetSession("sess_title")
			if err != nil {
				t.Fatalf("failed to get session: %v", err)
			}
			if !utf8.ValidString(rec.Title) {
				t.Fatalf("stored title is not valid UTF-8: %q", rec.Title)
			}
			if n := utf8.RuneCountInString(rec.Title); n > 40 {
				t.Errorf("stored title has %d runes, want <= 40: %q", n, rec.Title)
			}
			if rec.Title != tc.want {
				t.Errorf("stored title = %q, want %q", rec.Title, tc.want)
			}
			if strings.ContainsRune(rec.Title, utf8.RuneError) {
				t.Errorf("stored title contains replacement rune: %q", rec.Title)
			}
		})
	}
}

// B4: exports of a session whose title came from a multibyte first message are valid UTF-8.
func TestExportMultibyteTitleValidUTF8(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateSession("sess_export", "", "ollama", "codellama"); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	content := strings.Repeat("汉", 45) + "😀"
	if err := store.AppendMessage("sess_export", provider.Message{Role: "user", Content: content}); err != nil {
		t.Fatalf("failed to append message: %v", err)
	}

	rec, err := store.GetSession("sess_export")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}

	for _, format := range []ExportFormat{FormatJSON, FormatMarkdown} {
		data, err := Export(rec, ExportOptions{Format: format})
		if err != nil {
			t.Fatalf("export %s failed: %v", format, err)
		}
		if !utf8.Valid(data) {
			t.Errorf("%s export is not valid UTF-8", format)
		}
	}

	jsonData, err := Export(rec, ExportOptions{Format: FormatJSON})
	if err != nil {
		t.Fatalf("json export failed: %v", err)
	}
	var decoded JSONSessionExport
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("exported JSON does not decode: %v", err)
	}
	if decoded.Title != rec.Title {
		t.Errorf("exported title = %q, want %q", decoded.Title, rec.Title)
	}
}
