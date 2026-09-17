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

func TestTruncate_PreservesToolPairs(t *testing.T) {
	cm := NewContextManager(30) // Low limit to force dropping turn 1
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		// Turn 1
		{Role: "user", Content: "first turn with long query text that takes up tokens"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "shell_exec", Args: []byte(`{"command":"ls"}`)}}},
		{Role: "tool", Content: "file1.txt\nfile2.txt"},
		{Role: "assistant", Content: "here are the files"},
		// Turn 2
		{Role: "user", Content: "turn two"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_2", Name: "code_search", Args: []byte(`{"query":"test"}`)}}},
		{Role: "tool", Content: "match found"},
		{Role: "assistant", Content: "found it"},
	}

	result := cm.Truncate(history)

	// Result must start with system message
	if result[0].Role != "system" {
		t.Fatalf("expected system message first, got %s", result[0].Role)
	}

	// Result must not start non-system messages with "tool" role
	if len(result) > 1 && result[1].Role == "tool" {
		t.Fatalf("orphaned tool message at index 1!")
	}

	// If a message has tool role, its corresponding assistant tool call must be present
	for i, m := range result {
		if m.Role == "tool" {
			if i == 0 || len(result[i-1].ToolCalls) == 0 {
				t.Fatalf("tool message at index %d has no preceding assistant tool call!", i)
			}
		}
	}
}

func TestCompact(t *testing.T) {
	cm := NewContextManager(8192)
	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "turn 1"},
		{Role: "assistant", Content: "resp 1"},
		{Role: "user", Content: "turn 2"},
		{Role: "assistant", Content: "resp 2"},
		{Role: "user", Content: "turn 3"},
		{Role: "assistant", Content: "resp 3"},
	}

	compacted := cm.Compact(history, 1) // keep only last turn
	if len(compacted) >= len(history) {
		t.Errorf("expected compacted history to be smaller than original")
	}

	// Should have system message, summary user+assistant, and turn 3
	if compacted[0].Role != "system" {
		t.Errorf("expected system message first")
	}
	lastIdx := len(compacted) - 1
	if compacted[lastIdx].Content != "resp 3" {
		t.Errorf("expected last response 'resp 3', got %q", compacted[lastIdx].Content)
	}
}
