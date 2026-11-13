package agent

import (
	"strings"
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

// ReasoningContent is replayed into history, so it must count against the budget;
// otherwise Truncate concludes an oversized request fits.
func TestEstimateTokens_CountsReasoningContent(t *testing.T) {
	cm := NewContextManager(0)
	plain := cm.EstimateTokens(provider.Message{Role: "assistant", Content: "answer"})
	withReasoning := cm.EstimateTokens(provider.Message{
		Role:             "assistant",
		Content:          "answer",
		ReasoningContent: strings.Repeat("r", 400),
	})
	if withReasoning <= plain {
		t.Errorf("reasoning content not counted: plain=%d withReasoning=%d", plain, withReasoning)
	}
	if want := 400/4 + (len("answer")/4 + 4); withReasoning < want {
		t.Errorf("expected at least %d tokens with reasoning, got %d", want, withReasoning)
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
		{Role: "user", Content: "first turn with long query text that takes up tokens"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "shell_exec", Args: []byte(`{"command":"ls"}`)}}},
		{Role: "tool", Content: "file1.txt\nfile2.txt", ToolCallID: "call_1"},
		{Role: "assistant", Content: "here are the files"},
		{Role: "user", Content: "turn two"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_2", Name: "code_search", Args: []byte(`{"query":"test"}`)}}},
		{Role: "tool", Content: "match found", ToolCallID: "call_2"},
		{Role: "assistant", Content: "found it"},
	}

	result := cm.Truncate(history)

	if result[0].Role != "system" {
		t.Fatalf("expected system message first, got %s", result[0].Role)
	}

	if len(result) > 1 && result[1].Role == "tool" {
		t.Fatalf("orphaned tool message at index 1!")
	}

	for i, m := range result {
		if m.Role == "tool" {
			if i == 0 || len(result[i-1].ToolCalls) == 0 {
				t.Fatalf("tool message at index %d has no preceding assistant tool call!", i)
			}
		}
	}
}

func TestEstimateTextTokens_Empty(t *testing.T) {
	if got := estimateTextTokens(""); got != 0 {
		t.Fatalf("empty text should estimate 0 tokens, got %d", got)
	}
}

// CJK text tokenizes far worse than Latin prose: weighting it as prose would
// let a Japanese or Chinese transcript overflow the request budget.
func TestEstimateTextTokens_CJKHigherThanASCII(t *testing.T) {
	ascii := strings.Repeat("a", 60)
	cjk := strings.Repeat("漢", 60)

	asciiTokens := estimateTextTokens(ascii)
	cjkTokens := estimateTextTokens(cjk)
	if cjkTokens <= asciiTokens {
		t.Fatalf("CJK text must cost more than equal-length ASCII: cjk=%d ascii=%d", cjkTokens, asciiTokens)
	}
	if asciiTokens < 1 || cjkTokens < 1 {
		t.Fatalf("non-empty text must estimate at least one token: ascii=%d cjk=%d", asciiTokens, cjkTokens)
	}
}

// Source code and JSON are mostly punctuation; a plain 4-bytes-per-token
// estimate undercounts them badly enough to skip necessary truncation.
func TestEstimateTextTokens_PunctuationHeavyCode(t *testing.T) {
	plain := strings.Repeat("word ", 30)
	code := strings.Repeat(`a->b();{},[]`, 15)

	if est := estimateTextTokens(plain); est < 30 || est > 45 {
		t.Fatalf("plain prose estimate = %d, want roughly 4 chars/token (30..45)", est)
	}
	if codeTokens, plainTokens := estimateTextTokens(code), estimateTextTokens(plain); codeTokens <= plainTokens {
		t.Fatalf("punctuation-heavy code must cost more than plain words: code=%d plain=%d", codeTokens, plainTokens)
	}
}

func TestResolveBudget_ConfiguredOverrideWins(t *testing.T) {
	reg := provider.NewRegistry()
	reg.RegisterContextWindow("split-model", 128000)

	cm := NewContextManager(4096)
	got := cm.ResolveBudget(reg, "split-model")
	if got != 4096 || cm.MaxTokens != 4096 {
		t.Fatalf("configured budget must win: ResolveBudget=%d MaxTokens=%d", got, cm.MaxTokens)
	}
}

func TestResolveBudget_UsesModelWindow(t *testing.T) {
	reg := provider.NewRegistry()
	reg.RegisterContextWindow("test-model", 100000)

	cm := NewContextManager(0)
	got := cm.ResolveBudget(reg, "test-model")
	if got != 80000 {
		t.Fatalf("expected 80%% of the 100000-token window, got %d", got)
	}
	if cm.MaxTokens != 80000 {
		t.Fatalf("ResolveBudget must store the resolved budget, got %d", cm.MaxTokens)
	}
}

func TestResolveBudget_UnknownModelFallback(t *testing.T) {
	cm := NewContextManager(0)
	got := cm.ResolveBudget(provider.NewRegistry(), "totally-unknown")
	if got != 52428 { // 80% of the 65536 fallback window
		t.Fatalf("expected fallback budget 52428, got %d", got)
	}
}

func TestResolveBudget_ClampsToWindow(t *testing.T) {
	reg := provider.NewRegistry()
	reg.RegisterContextWindow("tiny", 2000)

	cm := NewContextManager(0)
	if got := cm.ResolveBudget(reg, "tiny"); got != 2000 {
		t.Fatalf("budget must not exceed a small window, got %d", got)
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

	compacted := cm.Compact(history, 1)
	if len(compacted) >= len(history) {
		t.Errorf("expected compacted history to be smaller than original")
	}

	if compacted[0].Role != "system" {
		t.Errorf("expected system message first")
	}
	lastIdx := len(compacted) - 1
	if compacted[lastIdx].Content != "resp 3" {
		t.Errorf("expected last response 'resp 3', got %q", compacted[lastIdx].Content)
	}
}
