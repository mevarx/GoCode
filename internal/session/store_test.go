package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

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
