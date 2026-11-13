package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
)

func TestNewSession(t *testing.T) {
	s := NewSession("gpt-4o")
	if s.Model() != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", s.Model())
	}
	if len(s.History()) != 0 {
		t.Errorf("expected empty history, got %d", len(s.History()))
	}
}

func TestSession_AddMessageAndHistory(t *testing.T) {
	s := NewSession("test-model")
	s.AddMessage(provider.Message{Role: "user", Content: "hello"})
	s.AddMessage(provider.Message{Role: "assistant", Content: "hi"})

	history := s.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Errorf("unexpected first message: %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "hi" {
		t.Errorf("unexpected second message: %+v", history[1])
	}
}

func TestSession_SetModel(t *testing.T) {
	s := NewSession("old-model")
	s.SetModel("new-model")
	if s.Model() != "new-model" {
		t.Errorf("expected 'new-model', got %q", s.Model())
	}
}

func TestSession_Clear(t *testing.T) {
	s := NewSession("test")
	s.AddMessage(provider.Message{Role: "user", Content: "hello"})
	s.AddMessage(provider.Message{Role: "assistant", Content: "hi"})
	s.Clear()

	if len(s.History()) != 0 {
		t.Errorf("expected empty history after clear, got %d", len(s.History()))
	}
}

func TestSession_LastAssistantMessage(t *testing.T) {
	s := NewSession("test")
	s.AddMessage(provider.Message{Role: "user", Content: "q1"})
	s.AddMessage(provider.Message{Role: "assistant", Content: "a1"})
	s.AddMessage(provider.Message{Role: "user", Content: "q2"})
	s.AddMessage(provider.Message{Role: "assistant", Content: "a2"})

	if got := s.LastAssistantMessage(); got != "a2" {
		t.Errorf("expected 'a2', got %q", got)
	}
}

func TestSession_SafetyCap(t *testing.T) {
	s := NewSession("test")
	s.AddMessage(provider.Message{Role: "system", Content: "system prompt"})

	for i := 0; i < 510; i++ {
		s.AddMessage(provider.Message{Role: "user", Content: "msg"})
	}

	history := s.History()
	if len(history) > 500 {
		t.Errorf("safety cap failed: expected <= 500 messages, got %d", len(history))
	}
	if history[0].Role != "system" {
		t.Errorf("expected system message preserved, got %q", history[0].Role)
	}
}

func TestSession_WithStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test_session_agent.db"
	st, err := session.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	rec, err := st.CreateSession("sess-agent", "Agent Test", "ollama", "qwen")
	if err != nil {
		t.Fatalf("failed to create session in store: %v", err)
	}

	s := NewSessionWithStore(rec.ID, rec.Provider, rec.Model, st)
	if s.ID() != "sess-agent" {
		t.Errorf("expected ID sess-agent, got %s", s.ID())
	}
	if s.Provider() != "ollama" {
		t.Errorf("expected provider ollama, got %s", s.Provider())
	}

	s.AddMessage(provider.Message{Role: "user", Content: "persist this"})

	loaded, err := st.GetSession("sess-agent")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "persist this" {
		t.Errorf("expected persisted message, got %+v", loaded.Messages)
	}
}

func TestSession_ModifiedFiles(t *testing.T) {
	s := NewSession("test-model")
	s.TrackModifiedFile("main.go")
	s.TrackModifiedFile("internal/agent/loop.go")
	s.TrackModifiedFile("main.go")

	files := s.ModifiedFiles()
	if len(files) != 2 {
		t.Fatalf("expected 2 unique modified files, got %d", len(files))
	}

	s.ClearModifiedFiles()
	if len(s.ModifiedFiles()) != 0 {
		t.Fatalf("expected 0 modified files after clear, got %d", len(s.ModifiedFiles()))
	}
}

func TestSession_AddMessagePersistsRedactedToolArgs(t *testing.T) {
	dbPath := t.TempDir() + "/test_redact_args.db"
	st, err := session.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	rec, err := st.CreateSession("sess-redact", "Redact Test", "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("failed to create session in store: %v", err)
	}

	s := NewSessionWithStore(rec.ID, rec.Provider, rec.Model, st)

	openaiKey := "sk-" + strings.Repeat("a1B2", 10)
	githubToken := "ghp_" + strings.Repeat("Z9x8", 10)
	args := json.RawMessage(`{"command":"curl -H 'Authorization: Bearer ` + openaiKey +
		`' https://example.com","api_key":"` + githubToken + `"}`)

	s.AddMessage(provider.Message{
		Role: "assistant",
		ToolCalls: []provider.ToolCall{{
			ID:   "call_secret",
			Name: "shell_exec",
			Args: args,
		}},
	})

	// The tool may still need the real arguments during this turn.
	history := s.History()
	if len(history) != 1 || len(history[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 message with 1 tool call in history, got %+v", history)
	}
	if string(history[0].ToolCalls[0].Args) != string(args) {
		t.Errorf("in-memory history was mutated; got %s", history[0].ToolCalls[0].Args)
	}
	if !strings.Contains(string(args), openaiKey) {
		t.Error("caller's args slice was mutated by AddMessage")
	}

	loaded, err := st.GetSession("sess-redact")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if len(loaded.Messages) != 1 || len(loaded.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected persisted tool call, got %+v", loaded.Messages)
	}
	persisted := string(loaded.Messages[0].ToolCalls[0].Args)
	if !strings.Contains(persisted, "***REDACTED***") {
		t.Errorf("persisted tool args are not redacted: %s", persisted)
	}
	for _, secret := range []string{openaiKey, githubToken} {
		if strings.Contains(persisted, secret) {
			t.Errorf("persisted tool args leaked secret %q: %s", secret, persisted)
		}
	}
	if !json.Valid([]byte(persisted)) {
		t.Errorf("persisted tool args are not valid JSON: %s", persisted)
	}
}
