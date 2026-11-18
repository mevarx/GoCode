package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// overflowProvider rejects real chat requests with a context-overflow error
// until its allowance is spent, then answers. Summary requests (a single user
// message carrying the compaction transcript) always succeed unless summaryErr
// is set, so the recovery path is observable in isolation.
type overflowProvider struct {
	mu           sync.Mutex
	histories    [][]provider.Message
	overflowLeft int
	summaryErr   bool
	text         string
}

func (p *overflowProvider) Name() string { return "overflow-fake" }

func (p *overflowProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *overflowProvider) Stream(_ context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	snapshot := make([]provider.Message, len(history))
	copy(snapshot, history)
	p.histories = append(p.histories, snapshot)

	summary := len(history) == 1 && history[0].Role == "user" &&
		strings.Contains(history[0].Content, "Transcript:")
	overflow := false
	if !summary && p.overflowLeft > 0 {
		p.overflowLeft--
		overflow = true
	}
	failSummary := summary && p.summaryErr
	text := p.text
	p.mu.Unlock()

	if overflow {
		return nil, fmt.Errorf(`provider "overflow-fake" returned status 400: {"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`)
	}
	if failSummary {
		return nil, fmt.Errorf("summary provider down")
	}

	ch := make(chan provider.StreamChunk, 2)
	if text != "" {
		ch <- provider.StreamChunk{Delta: text}
	}
	ch <- provider.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

// realCalls counts chat requests (more than one message); summaryCalls counts
// the single-message compaction requests.
func (p *overflowProvider) realCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, h := range p.histories {
		if len(h) > 1 {
			n++
		}
	}
	return n
}

func (p *overflowProvider) summaryCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, h := range p.histories {
		if len(h) == 1 && h[0].Role == "user" && strings.Contains(h[0].Content, "Transcript:") {
			n++
		}
	}
	return n
}

func (p *overflowProvider) lastHistory() []provider.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.histories) == 0 {
		return nil
	}
	return p.histories[len(p.histories)-1]
}

// autoCompactSession builds a session with a system prompt and count complete
// turns of roughly tokensPerTurn tokens each.
func autoCompactSession(tokensPerTurn, count int) *Session {
	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "system", Content: "system prompt"})
	for i := 0; i < count; i++ {
		sess.AddMessage(provider.Message{Role: "user", Content: fmt.Sprintf("request %d ", i) + contentTokens(tokensPerTurn)})
		sess.AddMessage(provider.Message{Role: "assistant", Content: "ok"})
	}
	return sess
}

// hasCompactSummary reports whether history carries either the model-written
// summary pair or the count-only placeholder pair.
func hasCompactSummary(history []provider.Message) bool {
	for _, msg := range history {
		if strings.HasPrefix(msg.Content, compactionSummaryHeading) ||
			strings.HasPrefix(msg.Content, "[Earlier conversation history:") {
			return true
		}
	}
	return false
}

func TestOverflowRecoveryCompactsAndRetriesOnce(t *testing.T) {
	p := &overflowProvider{overflowLeft: 1, text: "recovered"}
	sess := autoCompactSession(100, 3)

	engine := engineWith(p, sess)
	engine.Registry.RegisterContextWindow("m", 100000) // threshold far above the seeded history
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 150

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("recovered turn failed: %v", err)
	}
	if got := p.realCalls(); got != 2 {
		t.Fatalf("expected the original request plus one retry, got %d", got)
	}
	if got := p.summaryCalls(); got != 1 {
		t.Fatalf("expected one summary request, got %d", got)
	}
	if n := countNotices(events, "compacted automatically"); n != 1 {
		t.Fatalf("expected one automatic compaction notice, got %d", n)
	}
	if !hasCompactSummary(sess.History()) {
		t.Fatalf("compaction summary missing from session: %+v", sess.History())
	}
	if !hasCompactSummary(p.lastHistory()) {
		t.Fatalf("retried request did not carry the summary: %+v", p.lastHistory())
	}
}

func TestOverflowRecoveryIsBounded(t *testing.T) {
	p := &overflowProvider{overflowLeft: 99, text: "never delivered"}
	sess := autoCompactSession(100, 3)

	engine := engineWith(p, sess)
	engine.Registry.RegisterContextWindow("m", 100000)
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 150

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	err := engine.RunTurn(context.Background(), "go")
	if err == nil {
		t.Fatal("persistent overflow must fail the turn")
	}
	if got := p.realCalls(); got != 2 {
		t.Fatalf("recovery must not loop: %d chat requests, want 2", got)
	}
	if n := countNotices(events, "Context overflow again"); n != 1 {
		t.Fatalf("expected one bounded-recovery notice, got %d", n)
	}
}

func TestOverflowRecoveryUsesPlaceholderWhenSummaryFails(t *testing.T) {
	p := &overflowProvider{overflowLeft: 1, summaryErr: true, text: "recovered"}
	sess := autoCompactSession(100, 3)

	engine := engineWith(p, sess)
	engine.Registry.RegisterContextWindow("m", 100000)
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 150

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("placeholder recovery failed: %v", err)
	}
	if got := p.realCalls(); got != 2 {
		t.Fatalf("expected one retry, got %d chat requests", got)
	}
	if n := countNotices(events, "placeholder"); n != 1 {
		t.Fatalf("expected a placeholder notice, got %d", n)
	}
	if !hasCompactSummary(sess.History()) {
		t.Fatalf("placeholder compaction missing from session: %+v", sess.History())
	}
}

func TestAutoCompactRunsBeforeFirstRequest(t *testing.T) {
	p := &overflowProvider{text: "done"}
	sess := autoCompactSession(500, 8)

	engine := engineWith(p, sess)
	// 8k window: budget 6400, threshold 3200. The seeded history is above it.
	engine.Registry.RegisterContextWindow("m", 8000)
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 700

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if got := p.summaryCalls(); got != 1 {
		t.Fatalf("expected one pre-request summary, got %d", got)
	}
	if got := p.realCalls(); got != 1 {
		t.Fatalf("expected exactly one chat request, got %d", got)
	}
	if !hasCompactSummary(p.lastHistory()) {
		t.Fatalf("pre-request compaction did not reach the provider: %+v", p.lastHistory())
	}
	if n := countNotices(events, "compacted automatically"); n != 1 {
		t.Fatalf("expected one compaction notice, got %d", n)
	}
	if got := engine.ContextManager.Compactions.Load(); got != 1 {
		t.Fatalf("compaction counter = %d, want 1", got)
	}
}

func TestAutoCompactRunsAfterTurn(t *testing.T) {
	// The answer pushes the session over the threshold, so maintenance must
	// happen after the round finishes.
	p := &overflowProvider{text: contentTokens(600)}
	sess := autoCompactSession(600, 5)

	engine := engineWith(p, sess)
	engine.Registry.RegisterContextWindow("m", 8000) // budget 6400, threshold 3200
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 500

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if got := p.summaryCalls(); got != 1 {
		t.Fatalf("expected post-turn compaction, got %d summary requests", got)
	}
	if n := countNotices(events, "compacted automatically"); n != 1 {
		t.Fatalf("expected one compaction notice, got %d", n)
	}
}

func TestSlashTurnsNeverAutoCompact(t *testing.T) {
	p := &overflowProvider{text: "unused"}
	sess := autoCompactSession(500, 5)

	engine := engineWith(p, sess)
	engine.Registry.RegisterContextWindow("m", 8000)
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 700
	before := len(sess.History())

	if err := engine.RunTurn(context.Background(), "/stats"); err != nil {
		t.Fatalf("/stats failed: %v", err)
	}
	if got := p.summaryCalls(); got != 0 {
		t.Fatalf("slash turn triggered %d summary requests", got)
	}
	if got := p.realCalls(); got != 0 {
		t.Fatalf("slash turn reached the provider %d times", got)
	}
	if got := engine.ContextManager.Compactions.Load(); got != 0 {
		t.Fatalf("slash turn compacted %d times", got)
	}
	if got := len(sess.History()); got != before {
		t.Fatalf("slash turn rewrote history: %d -> %d", before, got)
	}
}

func TestTruncateDropEmitsNotice(t *testing.T) {
	p := &recordingProvider{text: "done"}
	sess := autoCompactSession(20, 4)

	engine := engineWith(p, sess)
	engine.ContextManager = NewContextManager(50) // tiny explicit budget

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if n := countNotices(events, "dropped without summarization"); n != 1 {
		t.Fatalf("expected exactly one truncation notice, got %d", n)
	}
}

func TestTruncateReportCountsDroppedTurns(t *testing.T) {
	history := toolCallHistory("system prompt", 5, 100)
	cm := NewContextManager(1)
	cm.MaxTokens = 1
	out, dropped := cm.TruncateReport(history)
	if dropped == 0 {
		t.Fatal("expected dropped turns")
	}
	if len(out) >= len(history) {
		t.Fatalf("truncate did not shrink history: %d -> %d", len(history), len(out))
	}
	assertHistoryInvariant(t, "truncated", out)
}

// Compaction must not lose the plan or scratchpad: they live in the system
// prompt, which the automatic path rebuilds after loading compacted history.
func TestAutoCompactionSurvivesPlanAndScratchpad(t *testing.T) {
	p := &overflowProvider{overflowLeft: 1, text: "recovered"}
	sess := autoCompactSession(100, 3)

	engine := engineWith(p, sess)
	engine.WorkspaceRoot = t.TempDir()
	engine.Registry.RegisterContextWindow("m", 100000)
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.AutoCompact = true
	engine.ContextManager.KeepRecentTokens = 150
	engine.Plan = tools.NewPlanStore()
	engine.Plan.Replace([]tools.PlanItem{{ID: "1", Text: "Keep me", Status: tools.PlanStatusInProgress}})
	engine.Scratchpad = tools.NewScratchpadStore()
	if err := engine.Scratchpad.Set("Goal: survive compaction"); err != nil {
		t.Fatalf("scratchpad set failed: %v", err)
	}
	sess.SetScratchpad(engine.Scratchpad.Render())
	engine.EnsureSystemPrompt()

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("recovered turn failed: %v", err)
	}

	var system string
	for _, msg := range p.lastHistory() {
		if msg.Role == "system" {
			system = msg.Content
		}
	}
	if !strings.Contains(system, "### Current Plan") || !strings.Contains(system, "- [~] (1) Keep me") {
		t.Fatalf("plan lost after compaction:\n%s", system)
	}
	if !strings.Contains(system, "### Task Scratchpad") || !strings.Contains(system, "Goal: survive compaction") {
		t.Fatalf("scratchpad lost after compaction:\n%s", system)
	}
}

func TestOverflowRecoveryWithoutAutoCompactFallsBackToTruncation(t *testing.T) {
	p := &overflowProvider{overflowLeft: 1, text: "recovered"}
	sess := autoCompactSession(100, 3)

	engine := engineWith(p, sess)
	engine.Registry.RegisterContextWindow("m", 100000)
	engine.ContextManager = NewContextManager(60) // truncation drops turns

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("truncation fallback failed: %v", err)
	}
	if got := p.summaryCalls(); got != 0 {
		t.Fatalf("auto-compact disabled but %d summaries were requested", got)
	}
	if got := p.realCalls(); got != 2 {
		t.Fatalf("expected the original request plus one truncated retry, got %d", got)
	}
	if n := countNotices(events, "automatic compaction is disabled"); n != 1 {
		t.Fatalf("expected one disabled-compaction notice, got %d", n)
	}
}
