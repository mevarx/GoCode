package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// summaryProvider records the requests it receives and replays a scripted
// summary. Only Stream matters; CompactWithSummary passes no tools.
type summaryProvider struct {
	mu        sync.Mutex
	histories [][]provider.Message
	summary   []string
	err       error
}

func (p *summaryProvider) Name() string { return "summary-fake" }

func (p *summaryProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *summaryProvider) Stream(_ context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	p.histories = append(p.histories, history)
	err := p.err
	parts := append([]string(nil), p.summary...)
	p.mu.Unlock()

	if err != nil {
		return nil, err
	}

	ch := make(chan provider.StreamChunk, len(parts)+1)
	for _, part := range parts {
		ch <- provider.StreamChunk{Delta: part}
	}
	ch <- provider.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

func (p *summaryProvider) lastHistory() []provider.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.histories) == 0 {
		return nil
	}
	return p.histories[len(p.histories)-1]
}

func (p *summaryProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.histories)
}

func summaryTestHistory() []provider.Message {
	return []provider.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Decision: use SQLite for storage."},
		{Role: "assistant", Content: "Agreed, SQLite it is."},
		{Role: "user", Content: "second turn"},
		{Role: "assistant", Content: "second response"},
		{Role: "user", Content: "latest instruction"},
		{Role: "assistant", Content: "latest response"},
	}
}

func TestCompactWithSummaryReplacesOldTurns(t *testing.T) {
	p := &summaryProvider{summary: []string{"Summary: ", "SQLite chosen for storage."}}
	cm := NewContextManager(8192)

	got, err := cm.CompactWithSummary(context.Background(), p, "m", summaryTestHistory(), 1)
	if err != nil {
		t.Fatalf("CompactWithSummary returned error: %v", err)
	}

	// The provider received a text-only summary request containing the
	// decisions it must carry forward.
	sent := p.lastHistory()
	if len(sent) != 1 || sent[0].Role != "user" {
		t.Fatalf("expected a single user summary prompt, got %+v", sent)
	}
	if !strings.Contains(sent[0].Content, "Decision: use SQLite for storage.") {
		t.Fatalf("summary prompt omitted earlier decisions:\n%s", sent[0].Content)
	}
	if strings.Contains(sent[0].Content, "latest instruction") {
		t.Fatal("summary prompt should only contain the omitted turns")
	}

	if got[0].Role != "system" || got[0].Content != "system prompt" {
		t.Fatalf("system prompt must be preserved, got %+v", got[0])
	}
	if got[1].Role != "user" || !strings.HasPrefix(got[1].Content, compactionSummaryHeading) {
		t.Fatalf("expected summary user message at index 1, got %+v", got[1])
	}
	if !strings.Contains(got[1].Content, "SQLite chosen for storage.") {
		t.Fatalf("summary text missing from history: %q", got[1].Content)
	}
	if got[2].Role != "assistant" || got[2].Content != compactionSummaryAck {
		t.Fatalf("expected a fixed acknowledgement, got %+v", got[2])
	}
	if got[len(got)-2].Content != "latest instruction" || got[len(got)-1].Content != "latest response" {
		t.Fatalf("last turn was not preserved: %+v", got[len(got)-2:])
	}
	if len(got) >= len(summaryTestHistory()) {
		t.Fatalf("expected compaction to shrink history: %d -> %d", len(summaryTestHistory()), len(got))
	}
	for _, msg := range got {
		if strings.Contains(msg.Content, "Decision: use SQLite for storage.") {
			t.Fatalf("omitted turn leaked into compacted history: %q", msg.Content)
		}
	}

	assertHistoryInvariant(t, "summary compact", got)
}

func TestCompactWithSummaryKeepsScratchpadInSystemPrompt(t *testing.T) {
	p := &summaryProvider{summary: []string{"condensed"}}
	cm := NewContextManager(8192)

	history := summaryTestHistory()
	history[0].Content = "system prompt\n\n## Task Scratchpad\nGoal: finish C1"

	got, err := cm.CompactWithSummary(context.Background(), p, "m", history, 1)
	if err != nil {
		t.Fatalf("CompactWithSummary returned error: %v", err)
	}
	if !strings.Contains(got[0].Content, "## Task Scratchpad") || !strings.Contains(got[0].Content, "Goal: finish C1") {
		t.Fatalf("scratchpad-bearing system prompt was not preserved: %q", got[0].Content)
	}
}

func TestCompactWithSummaryRedactsToolOutputAndArgs(t *testing.T) {
	p := &summaryProvider{summary: []string{"summary"}}
	cm := NewContextManager(8192)

	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "turn 1"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{
			ID:   "c1",
			Name: "shell_exec",
			Args: []byte(`{"password":"hunter2hunter2"}`),
		}}},
		{Role: "tool", Content: "key: sk-abcdefghijklmnopqrstuvwx", ToolCallID: "c1"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "turn 2"},
		{Role: "assistant", Content: "resp 2"},
		{Role: "user", Content: "turn 3"},
		{Role: "assistant", Content: "resp 3"},
	}

	if _, err := cm.CompactWithSummary(context.Background(), p, "m", history, 2); err != nil {
		t.Fatalf("CompactWithSummary returned error: %v", err)
	}

	sent := p.lastHistory()
	if len(sent) != 1 {
		t.Fatalf("expected one summary prompt, got %d", len(sent))
	}
	for _, secret := range []string{"hunter2hunter2", "sk-abcdefghijklmnopqrstuvwx"} {
		if strings.Contains(sent[0].Content, secret) {
			t.Fatalf("summary prompt leaked %q:\n%s", secret, sent[0].Content)
		}
	}
}

func TestCompactWithSummaryPropagatesProviderError(t *testing.T) {
	p := &summaryProvider{err: errors.New("provider down")}
	cm := NewContextManager(8192)

	got, err := cm.CompactWithSummary(context.Background(), p, "m", summaryTestHistory(), 1)
	if err == nil {
		t.Fatal("expected a provider error")
	}
	if got != nil {
		t.Fatalf("expected no history on error, got %+v", got)
	}
	if !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("error should carry the provider failure: %v", err)
	}
}

func TestCompactWithSummaryRejectsEmptySummary(t *testing.T) {
	p := &summaryProvider{}
	cm := NewContextManager(8192)

	if _, err := cm.CompactWithSummary(context.Background(), p, "m", summaryTestHistory(), 1); err == nil {
		t.Fatal("expected an empty-summary error")
	}
}

func TestCompactWithSummaryNoOpWhenNothingToOmit(t *testing.T) {
	p := &summaryProvider{summary: []string{"unused"}}
	cm := NewContextManager(8192)

	history := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "only turn"},
		{Role: "assistant", Content: "only response"},
	}

	got, err := cm.CompactWithSummary(context.Background(), p, "m", history, 2)
	if err != nil {
		t.Fatalf("CompactWithSummary returned error: %v", err)
	}
	if len(got) != len(history) {
		t.Fatalf("history should be unchanged, got %d messages", len(got))
	}
	if p.calls() != 0 {
		t.Fatal("provider must not be called when no turn would be omitted")
	}
}

// /compact silently falls back to placeholder compaction when the provider
// cannot produce a summary, so the command keeps working offline.
func TestHandleCommand_CompactFallsBackOnProviderError(t *testing.T) {
	p := &summaryProvider{err: errors.New("broken summary")}
	reg := provider.NewRegistry()
	reg.Register(p)

	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 1"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 1"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 2"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 2"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 3"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 3"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 4"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 4"})

	cmdCtx := CommandContext{
		Session:        sess,
		Registry:       reg,
		ContextManager: NewContextManager(8192),
	}

	res := HandleCommand(context.Background(), cmdCtx, "/compact")
	if !res.Handled || res.Error != nil {
		t.Fatalf("expected handled success, got %+v", res)
	}
	if !strings.Contains(res.Output, "Compacted history") {
		t.Fatalf("expected compact output, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "placeholder compaction") {
		t.Fatalf("expected fallback notice, got: %s", res.Output)
	}
	if len(sess.History()) >= 8 {
		t.Fatalf("fallback did not compact: %d messages", len(sess.History()))
	}
}

func TestHandleCommand_CompactUsesProviderSummary(t *testing.T) {
	p := &summaryProvider{summary: []string{"summary of early work"}}
	reg := provider.NewRegistry()
	reg.Register(p)

	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 1"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 1"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 2"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 2"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 3"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 3"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 4"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 4"})

	cmdCtx := CommandContext{
		Session:        sess,
		Registry:       reg,
		ContextManager: NewContextManager(8192),
	}

	res := HandleCommand(context.Background(), cmdCtx, "/compact")
	if !res.Handled || res.Error != nil {
		t.Fatalf("expected handled success, got %+v", res)
	}
	history := sess.History()
	found := false
	for _, msg := range history {
		if strings.Contains(msg.Content, "summary of early work") {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider summary not in history: %+v", history)
	}
	if strings.Contains(res.Output, "placeholder compaction") {
		t.Fatalf("successful summary must not report a fallback: %s", res.Output)
	}
}
