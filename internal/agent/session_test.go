package agent

import (
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
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
