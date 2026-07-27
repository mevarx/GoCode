package agent

import (
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

func TestNewContextManager_DefaultTokens(t *testing.T) {
	cm := NewContextManager(0)
	if cm.MaxTokens != 8192 {
		t.Errorf("expected default 8192, got %d", cm.MaxTokens)
	}
}

func TestNewContextManager_CustomTokens(t *testing.T) {
	cm := NewContextManager(4096)
	if cm.MaxTokens != 4096 {
		t.Errorf("expected 4096, got %d", cm.MaxTokens)
	}
}

func TestEstimateTokens_SimpleMessage(t *testing.T) {
	cm := NewContextManager(0)
	msg := provider.Message{Role: "user", Content: "hello world"}
	tokens := cm.EstimateTokens(msg)
	if tokens < 1 {
		t.Errorf("expected at least 1 token, got %d", tokens)
	}
}

func TestEstimateTokens_WithToolCalls(t *testing.T) {
	cm := NewContextManager(0)
	msg := provider.Message{
		Role:    "assistant",
		Content: "calling tool",
		ToolCalls: []provider.ToolCall{
			{ID: "1", Name: "shell_exec", Args: []byte(`{"command":"ls"}`)},
		},
	}
	tokens := cm.EstimateTokens(msg)
	plain := cm.EstimateTokens(provider.Message{Role: "assistant", Content: "calling tool"})
	if tokens <= plain {
		t.Errorf("tool calls should increase token count: %d vs %d", tokens, plain)
	}
}

func TestTruncate_NoOpUnderLimit(t *testing.T) {
	cm := NewContextManager(10000)
	history := []provider.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	result := cm.Truncate(history)
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result))
	}
}

func TestTruncate_TruncatesOverLimit(t *testing.T) {
	cm := NewContextManager(1)
	history := []provider.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "message one"},
		{Role: "assistant", Content: "response one"},
		{Role: "user", Content: "message two"},
		{Role: "assistant", Content: "response two"},
	}
	result := cm.Truncate(history)
	if len(result) >= len(history) {
		t.Errorf("expected truncation, got %d messages (same as input %d)", len(result), len(history))
	}
}

func TestTruncate_PreservesSystemMessage(t *testing.T) {
	cm := NewContextManager(1)
	history := []provider.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "msg1"},
		{Role: "assistant", Content: "resp1"},
		{Role: "user", Content: "msg2"},
		{Role: "assistant", Content: "resp2"},
	}
	result := cm.Truncate(history)
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	if result[0].Role != "system" {
		t.Errorf("expected system message first, got %q", result[0].Role)
	}
}

func TestTruncate_EmptyHistory(t *testing.T) {
	cm := NewContextManager(0)
	result := cm.Truncate(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
}
