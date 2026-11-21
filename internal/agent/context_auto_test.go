package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// contentTokens returns text whose estimateTextTokens is about n tokens:
// ASCII letters count one token per four runes.
func contentTokens(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("x", n*4)
}

// registryWithWindow registers a provider and a model window so ResolveBudget
// and AutoCompactThreshold have real numbers to work from.
func registryWithWindow(model string, window int) *provider.Registry {
	reg := provider.NewRegistry()
	reg.Register(&recordingProvider{text: "ok"})
	reg.RegisterContextWindow(model, window)
	return reg
}

func TestAutoCompactThresholdMath(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		window     int
		want       int
	}{
		// 80% budget, reserve = 15% of the window.
		{name: "128k window", window: 128000, want: 83200},
		{name: "200k window", window: 200000, want: 130000},
		// 8k window: budget 6400, reserve raised to the 16384 floor and then
		// clamped to half the budget, so the threshold stays positive.
		{name: "8k window", window: 8000, want: 3200},
		// A window smaller than the 4096 budget floor still yields a positive
		// threshold.
		{name: "3k window", window: 3000, want: 1500},
		// A configured budget wins over the window and the reserve clamps to
		// half of it.
		{name: "configured budget", configured: 4096, window: 128000, want: 2048},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := registryWithWindow("m", tc.window)
			cm := NewContextManager(tc.configured)
			cm.ResolveBudget(reg, "m")
			if got := cm.AutoCompactThreshold(reg.ContextWindow("m")); got != tc.want {
				t.Fatalf("threshold = %d, want %d (budget %d)", got, tc.want, cm.MaxTokens)
			}
		})
	}
}

func TestAutoCompactThresholdNeverNegative(t *testing.T) {
	for _, window := range []int{1, 100, 512, 1024, 2048, 4096, 8192} {
		cm := NewContextManager(0)
		cm.ResolveBudget(registryWithWindow("m", window), "m")
		if got := cm.AutoCompactThreshold(window); got < 0 {
			t.Fatalf("window %d produced negative threshold %d", window, got)
		}
	}
}

// toolCallHistory builds count complete tool turns after an optional leading
// system prompt, each with a user message, an assistant call, one tool result
// carrying resultTokens of output, and a closing assistant message.
func toolCallHistory(system string, count, resultTokens int) []provider.Message {
	var history []provider.Message
	if system != "" {
		history = append(history, provider.Message{Role: "system", Content: system})
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("call-%d", i)
		history = append(history,
			provider.Message{Role: "user", Content: fmt.Sprintf("task %d ", i) + contentTokens(100)},
			provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
				ID:   id,
				Name: "file_read",
				Args: json.RawMessage(`{"path":"a.go"}`),
			}}},
			provider.Message{Role: "tool", ToolCallID: id, Content: contentTokens(resultTokens)},
			provider.Message{Role: "assistant", Content: "done"},
		)
	}
	return history
}

func TestAutoCompactCutPointKeepsWholeTurns(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 6000

	history := toolCallHistory("system prompt", 5, 2000)
	_, nonSystem := splitSystemMessages(history)
	turns := groupIntoTurns(nonSystem)
	cut := cm.autoCompactCutPoint(turns)
	if cut <= 0 || cut >= len(turns) {
		t.Fatalf("expected an interior cut point, got %d of %d turns", cut, len(turns))
	}
	if !validCutPoint(turns, cut) {
		t.Fatalf("cut %d is not a user-message boundary", cut)
	}
	if role := turns[cut].Messages[0].Role; role != "user" {
		t.Fatalf("retained window starts with %q, want user", role)
	}

	retained := 0
	for _, turn := range turns[cut:] {
		retained += turn.Tokens(cm)
	}
	if retained > cm.KeepRecentTokens {
		t.Errorf("retained tail is %d tokens, over the %d limit", retained, cm.KeepRecentTokens)
	}

	// Every call in the dropped span has its result in the same span.
	for i := 0; i < cut; i++ {
		results := make(map[string]bool)
		for _, msg := range turns[i].Messages {
			if msg.Role == "tool" {
				results[msg.ToolCallID] = true
			}
		}
		for _, msg := range turns[i].Messages {
			for _, call := range msg.ToolCalls {
				if !results[call.ID] {
					t.Fatalf("turn %d drops call %q without its result", i, call.ID)
				}
			}
		}
	}

	// The retained window is wire-valid: no orphaned result at the boundary.
	window := append([]provider.Message{history[0]}, flattenTurns(turns[cut:])...)
	assertHistoryInvariant(t, "retained window", window)
}

func flattenTurns(turns []Turn) []provider.Message {
	var out []provider.Message
	for _, turn := range turns {
		out = append(out, turn.Messages...)
	}
	return out
}

func TestAutoCompactCutPointHandlesMidTurnStart(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 50

	// A truncated transcript can start inside a tool block.
	history := []provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "orphan", Name: "file_read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "orphan", Content: contentTokens(3000)},
		{Role: "user", Content: "first kept instruction"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "latest instruction"},
		{Role: "assistant", Content: "latest answer"},
	}

	turns := groupIntoTurns(history)
	cut := cm.autoCompactCutPoint(turns)
	if cut != 1 {
		t.Fatalf("expected cut at 1 to drop the partial leading turn, got %d", cut)
	}
	retained := flattenTurns(turns[cut:])
	if retained[0].Role != "user" {
		t.Fatalf("retained window starts with %q, want user", retained[0].Role)
	}
	assertHistoryInvariant(t, "mid-turn cut", retained)
}

func TestAutoCompactCutPointSkipsWhenNoCutExists(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 1

	// One turn only: compacting it would drop the live turn.
	single := []provider.Message{
		{Role: "user", Content: contentTokens(5000)},
		{Role: "assistant", Content: "answer"},
	}
	if cut := cm.autoCompactCutPoint(groupIntoTurns(single)); cut != 0 {
		t.Fatalf("single-turn history produced cut %d, want 0", cut)
	}
	if cut := cm.autoCompactCutPoint(nil); cut != 0 {
		t.Fatalf("empty history produced cut %d, want 0", cut)
	}
}

func toolResultHistory(recentTokens int, oldResults ...int) []provider.Message {
	var history []provider.Message
	if len(oldResults) > 0 {
		calls := make([]provider.ToolCall, len(oldResults))
		for i := range oldResults {
			calls[i] = provider.ToolCall{ID: fmt.Sprintf("old-%d", i), Name: "shell_exec", Args: json.RawMessage(`{}`)}
		}
		history = append(history, provider.Message{Role: "assistant", ToolCalls: calls})
		for i, tokens := range oldResults {
			history = append(history, provider.Message{
				Role:       "tool",
				ToolCallID: fmt.Sprintf("old-%d", i),
				Content:    contentTokens(tokens),
			})
		}
	}
	history = append(history,
		provider.Message{Role: "assistant", Content: "moving on"},
		provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "recent", Name: "file_read", Args: json.RawMessage(`{}`)}}},
		provider.Message{Role: "tool", ToolCallID: "recent", Content: contentTokens(recentTokens)},
	)
	return history
}

func TestPruneToolResultsProtectsRecentOutput(t *testing.T) {
	// Both results together are inside the newest 40k tokens of tool output.
	history := toolResultHistory(20000, 15000)
	pruned, saved := PruneToolResults(history)
	if saved != 0 {
		t.Fatalf("recent tool output must be protected, pruned %d tokens", saved)
	}
	if len(pruned) != len(history) || pruned[1].Content != history[1].Content {
		t.Fatal("protected history was modified")
	}
}

func TestPruneToolResultsElidesOldOversizedOutput(t *testing.T) {
	// The recent result alone exceeds the protection window, so the old one
	// becomes a candidate.
	history := toolResultHistory(45000, 30000)
	pruned, saved := PruneToolResults(history)
	if saved < pruneSavingsFloor {
		t.Fatalf("savings %d below floor %d", saved, pruneSavingsFloor)
	}
	if !strings.Contains(pruned[1].Content, "output truncated") {
		t.Fatalf("old tool output was not elided: %q", pruned[1].Content)
	}
	if !strings.Contains(pruned[1].Content, "~30000 tokens") {
		t.Fatalf("placeholder must report the estimate: %q", pruned[1].Content)
	}
	if pruned[1].Role != "tool" || pruned[1].ToolCallID != history[1].ToolCallID {
		t.Fatalf("pruning changed tool identity: %+v", pruned[1])
	}
	if pruned[len(pruned)-1].Content != history[len(history)-1].Content {
		t.Fatal("recent tool result was modified")
	}
	assertHistoryInvariant(t, "pruned history", pruned)
}

func TestPruneToolResultsLeavesShortResultsAlone(t *testing.T) {
	history := toolResultHistory(45000, 40)
	pruned, saved := PruneToolResults(history)
	if saved != 0 || pruned[1].Content != history[1].Content {
		t.Fatalf("results below %d tokens must not be pruned (saved %d)", pruneMinResultTokens, saved)
	}
}

func TestPruneToolResultsHonorsSavingsFloor(t *testing.T) {
	// Three old results of ~100 tokens each: below the 8192-token floor.
	history := toolResultHistory(45000, 100, 100, 100)
	pruned, saved := PruneToolResults(history)
	if saved != 0 {
		t.Fatalf("savings %d below the floor must be skipped", saved)
	}
	for i := range history {
		if pruned[i].Content != history[i].Content {
			t.Fatalf("history changed despite insufficient savings at index %d", i)
		}
	}
}

func TestIsContextOverflowErrorDialects(t *testing.T) {
	overflows := []struct {
		name string
		err  string
	}{
		{
			name: "openai code",
			err:  `provider "openai" returned status 400: {"error":{"message":"This model's maximum context length is 128000 tokens. However, you requested 135000 tokens (130000 in the messages, 5000 in the completion).","type":"invalid_request_error","param":null,"code":"context_length_exceeded"}}`,
		},
		{
			name: "openai prose only",
			err:  `stream error: provider "openai" returned status 400: maximum context length exceeded`,
		},
		{
			name: "anthropic",
			err:  `provider "anthropic" returned status 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
		},
		{
			name: "ollama",
			err:  `provider "ollama" returned status 400: the input length exceeds the context length`,
		},
		{
			name: "generic 400",
			err:  `provider "groq" returned status 400: bad request: input token count exceeds the maximum number of tokens allowed`,
		},
		{
			name: "gemini invalid argument",
			err:  `provider "gemini" returned status 400: {"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count (210000) exceeds the maximum number of tokens allowed (200000)."}}`,
		},
	}

	for _, tc := range overflows {
		t.Run(tc.name, func(t *testing.T) {
			if !isContextOverflowError(fmt.Errorf("%s", tc.err)) {
				t.Fatalf("not classified as overflow: %s", tc.err)
			}
		})
	}

	notOverflows := []string{
		`provider "openai" returned status 401: {"error":{"message":"Incorrect API key provided"}}`,
		`provider "openai" returned status 400: {"error":{"message":"The model gpt-5 does not exist"}}`,
		`provider "anthropic" returned status 429: rate limit exceeded, too many requests`,
		`provider "x" returned status 500: internal server error`,
		`stream chunk error: context canceled`,
	}
	for _, err := range notOverflows {
		if isContextOverflowError(fmt.Errorf("%s", err)) {
			t.Fatalf("misclassified as overflow: %s", err)
		}
	}
	if isContextOverflowError(nil) {
		t.Fatal("nil error classified as overflow")
	}
}

func TestAutoCompactHistorySummarizesDroppedSpan(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 3000

	history := toolCallHistory("system prompt", 4, 2000)
	p := &summaryProvider{summary: []string{"Summary: earlier work on the parser."}}

	outcome := cm.AutoCompactHistory(context.Background(), p, "m", history)
	if !outcome.Changed || !outcome.Summarized {
		t.Fatalf("expected a summarized compaction, got %+v", outcome)
	}
	if outcome.DroppedTurns == 0 {
		t.Fatal("expected dropped turns")
	}
	if outcome.History[0].Role != "system" || outcome.History[0].Content != "system prompt" {
		t.Fatalf("system prompt was not preserved: %+v", outcome.History[0])
	}
	if !strings.HasPrefix(outcome.History[1].Content, compactionSummaryHeading) {
		t.Fatalf("expected a summary pair after the system prompt, got %+v", outcome.History[1])
	}
	if !strings.Contains(outcome.History[1].Content, "earlier work on the parser") {
		t.Fatalf("summary text missing: %q", outcome.History[1].Content)
	}
	if got := cm.Compactions.Load(); got != 1 {
		t.Fatalf("compaction counter = %d, want 1", got)
	}
	assertHistoryInvariant(t, "auto compacted history", outcome.History)
}

func TestAutoCompactHistoryFallsBackToPlaceholder(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 3000

	history := toolCallHistory("system prompt", 4, 2000)
	p := &summaryProvider{err: fmt.Errorf("provider down")}

	outcome := cm.AutoCompactHistory(context.Background(), p, "m", history)
	if !outcome.Changed || outcome.Summarized {
		t.Fatalf("expected placeholder compaction, got %+v", outcome)
	}
	if outcome.SummaryErr == nil {
		t.Fatal("expected the summary error to be reported")
	}
	if !strings.Contains(outcome.History[1].Content, "compacted") {
		t.Fatalf("placeholder summary missing: %+v", outcome.History[1])
	}
	assertHistoryInvariant(t, "placeholder history", outcome.History)
}

func TestAutoCompactHistorySkipsWithoutCutPoint(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 1

	single := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: contentTokens(1000)},
		{Role: "assistant", Content: "answer"},
	}
	p := &summaryProvider{summary: []string{"should not run"}}
	outcome := cm.AutoCompactHistory(context.Background(), p, "m", single)
	if outcome.Changed || p.calls() != 0 {
		t.Fatalf("compacted a single-turn history: %+v (calls %d)", outcome, p.calls())
	}
}

func TestAutoCompactHistoryPrunesWithoutCutPoint(t *testing.T) {
	cm := NewContextManager(0)
	cm.KeepRecentTokens = 100000 // no turn needs to be dropped

	// No user message, so the history is a single turn: there is no valid cut,
	// but old oversized tool output can still be elided.
	history := toolResultHistory(45000, 30000)
	outcome := cm.AutoCompactHistory(context.Background(), nil, "m", history)
	if !outcome.Changed || outcome.PrunedTokens < pruneSavingsFloor {
		t.Fatalf("expected a pruning-only pass, got %+v", outcome)
	}
	if outcome.DroppedTurns != 0 || outcome.Summarized {
		t.Fatalf("pruning-only pass must not summarize: %+v", outcome)
	}
	if !strings.Contains(outcome.History[1].Content, "output truncated") {
		t.Fatalf("old tool output not pruned: %q", outcome.History[1].Content)
	}
}
