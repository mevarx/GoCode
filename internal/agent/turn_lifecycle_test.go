package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// usageProvider answers one request with text and provider-reported usage, then
// finishes. A turn drives it once, so the reported counts describe the turn.
type usageProvider struct {
	promptTokens     int
	completionTokens int
	// delay delays the first chunk so a test can observe a non-zero elapsed.
	delay time.Duration
}

func (p *usageProvider) Name() string { return "usage-fake" }

func (p *usageProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *usageProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	ch := make(chan provider.StreamChunk, 4)
	ch <- provider.StreamChunk{Delta: "answer"}
	ch <- provider.StreamChunk{Usage: &provider.Usage{
		PromptTokens:     p.promptTokens,
		CompletionTokens: p.completionTokens,
	}}
	ch <- provider.StreamChunk{Done: true, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

// abortProvider blocks mid-stream until the turn's context is cancelled, so the
// only way out of the turn is the abort path.
type abortProvider struct{ started chan struct{} }

func (p *abortProvider) Name() string { return "abort-fake" }

func (p *abortProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *abortProvider) Stream(ctx context.Context, _ string, _ []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk)
	go func() {
		defer close(ch)
		close(p.started)
		<-ctx.Done()
	}()
	return ch, nil
}

func countEventKind(events []LoopEvent, kind LoopEventKind) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func indexOfEventKind(events []LoopEvent, kind LoopEventKind) int {
	for i, ev := range events {
		if ev.Kind == kind {
			return i
		}
	}
	return -1
}

// oneTurnEnd returns the single end event, failing when the loop emitted zero
// or several: "exactly once" is the contract, not "at least once".
func oneTurnEnd(t *testing.T, events []LoopEvent) LoopEvent {
	t.Helper()
	if got := countEventKind(events, EventTurnEnd); got != 1 {
		t.Fatalf("expected exactly one EventTurnEnd, got %d (%v)", got, eventKindSequence(events))
	}
	return events[indexOfEventKind(events, EventTurnEnd)]
}

func eventKindSequence(events []LoopEvent) []LoopEventKind {
	kinds := make([]LoopEventKind, len(events))
	for i, ev := range events {
		kinds[i] = ev.Kind
	}
	return kinds
}

// A normal turn: start first, deltas after it, end exactly once and last.
func TestTurnStartPrecedesDeltasAndEndClosesTurn(t *testing.T) {
	events, _, err := turnEvents(t, &usageProvider{promptTokens: 1200, completionTokens: 40})
	if err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	if len(events) == 0 || events[0].Kind != EventTurnStart {
		t.Fatalf("first event must be EventTurnStart, got %v", eventKindSequence(events))
	}
	if got := countEventKind(events, EventTurnStart); got != 1 {
		t.Errorf("expected exactly one EventTurnStart, got %d", got)
	}

	start := indexOfEventKind(events, EventTurnStart)
	delta := indexOfEventKind(events, EventDelta)
	if delta < 0 {
		t.Fatalf("no delta reported: %v", eventKindSequence(events))
	}
	if delta < start {
		t.Errorf("delta at %d preceded turn start at %d", delta, start)
	}
	if last := events[len(events)-1]; last.Kind != EventTurnEnd {
		t.Errorf("last event must be EventTurnEnd, got %v", last.Kind)
	}

	end := oneTurnEnd(t, events)
	if end.Err != nil {
		t.Errorf("normal turn reported an error: %v", end.Err)
	}
	if end.Metrics == nil {
		t.Fatal("EventTurnEnd carried no metrics")
	}
}

// A cancelled turn still closes exactly once, with the cancellation attached.
func TestTurnEndEmittedOnceOnAbort(t *testing.T) {
	p := &abortProvider{started: make(chan struct{})}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	var mu sync.Mutex
	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.RunTurn(ctx, "go") }()

	<-p.started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	end := oneTurnEnd(t, events)
	if !errors.Is(end.Err, context.Canceled) {
		t.Errorf("turn end error = %v, want context.Canceled", end.Err)
	}
	if end.Metrics == nil {
		t.Fatal("aborted turn carried no metrics")
	}
}

// A loop-guard stop is a normal return, but the turn still closes exactly once
// and reports the calls it attempted.
func TestTurnEndEmittedOnceOnGuardStop(t *testing.T) {
	p := &recordingProvider{toolCalls: []provider.ToolCall{{
		ID:   "x",
		Name: "file_read",
		Args: []byte(`{"path":"missing.txt"}`),
	}}}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	end := oneTurnEnd(t, events)
	if end.Err != nil {
		t.Errorf("guard stop reported an error: %v", end.Err)
	}
	if end.Metrics == nil {
		t.Fatal("guard stop carried no metrics")
	}
	if end.Metrics.ToolCalls < 2 {
		t.Errorf("tool calls = %d, want at least 2 before the guard stopped the turn", end.Metrics.ToolCalls)
	}
}

// A provider failure closes the turn once, carrying the error.
func TestTurnEndEmittedOnceOnProviderError(t *testing.T) {
	sess := NewSession("m")
	engine := engineWith(&errProvider{}, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err == nil {
		t.Fatal("expected the provider error to be returned")
	}

	end := oneTurnEnd(t, events)
	if end.Err == nil || !strings.Contains(end.Err.Error(), "boom") {
		t.Errorf("turn end error = %v, want the provider failure", end.Err)
	}
	if end.Metrics == nil {
		t.Fatal("failed turn carried no metrics")
	}
	if end.Metrics.PromptTokens < 1 {
		t.Errorf("prompt tokens = %d, want the estimated prompt size", end.Metrics.PromptTokens)
	}
}

// Slash commands are turns too: they open and close, and report no tokens
// because no model was called.
func TestTurnLifecycleForSlashCommand(t *testing.T) {
	sess := NewSession("m")
	engine := engineWith(&scriptedProvider{}, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "/help"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	if events[0].Kind != EventTurnStart {
		t.Errorf("first event = %v, want EventTurnStart", events[0].Kind)
	}
	end := oneTurnEnd(t, events)
	if end.Metrics == nil {
		t.Fatal("command turn carried no metrics")
	}
	if end.Metrics.PromptTokens != 0 || end.Metrics.CompletionTokens != 0 {
		t.Errorf("command turn reported tokens: %+v", *end.Metrics)
	}
}

// Blank input is not a turn: no start, no end, no provider call.
func TestBlankInputEmitsNoTurnEvents(t *testing.T) {
	p := &recordingProvider{text: "unused"}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "   "); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("blank input produced events: %v", eventKindSequence(events))
	}
	if p.callCount() != 0 {
		t.Errorf("blank input called the provider %d times", p.callCount())
	}
}

// Provider-reported usage supersedes the local estimate and feeds context
// accounting.
func TestTurnMetricsUseProviderReportedUsage(t *testing.T) {
	p := &usageProvider{promptTokens: 12000, completionTokens: 400, delay: 2 * time.Millisecond}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	end := oneTurnEnd(t, events)
	m := end.Metrics
	if m == nil {
		t.Fatal("turn end carried no metrics")
	}
	if !m.ProviderReported {
		t.Error("ProviderReported = false, want true when usage arrived")
	}
	if m.PromptTokens != 12000 || m.CompletionTokens != 400 {
		t.Errorf("tokens = %d/%d, want the provider's 12000/400", m.PromptTokens, m.CompletionTokens)
	}
	// The local estimate for "go" is a handful of tokens, so the effective
	// context size must come from the provider report.
	if m.ContextTokens != 12400 {
		t.Errorf("ContextTokens = %d, want 12400 (provider prompt+completion)", m.ContextTokens)
	}
	if m.ContextBudget <= 0 {
		t.Errorf("ContextBudget = %d, want a resolved budget", m.ContextBudget)
	}
	if m.Elapsed < 2*time.Millisecond {
		t.Errorf("Elapsed = %v, want at least the 2ms the provider slept", m.Elapsed)
	}
	// The same observation drives the ContextManager's own accounting.
	if got := engine.ContextManager.ProviderReportedContext; got != 12400 {
		t.Errorf("ContextManager.ProviderReportedContext = %d, want 12400", got)
	}
}

// Without usage the turn reports estimates and says so.
func TestTurnMetricsFallBackToEstimates(t *testing.T) {
	events, _, err := turnEvents(t, &scriptedProvider{chunks: []provider.StreamChunk{
		{Delta: "hello there"},
		{Done: true, FinishReason: "stop"},
	}})
	if err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	m := oneTurnEnd(t, events).Metrics
	if m == nil {
		t.Fatal("turn end carried no metrics")
	}
	if m.ProviderReported {
		t.Error("ProviderReported = true without any usage chunk")
	}
	if m.PromptTokens < 1 || m.CompletionTokens < 1 {
		t.Errorf("estimated tokens = %d/%d, want positive estimates", m.PromptTokens, m.CompletionTokens)
	}
	if m.ContextTokens < m.PromptTokens {
		t.Errorf("ContextTokens = %d, want at least the prompt estimate %d", m.ContextTokens, m.PromptTokens)
	}
}

// A provider can report fewer tokens than the local estimate (different
// tokenizer); the effective context size must take the larger, so a small
// report cannot hide a large window.
func TestTurnMetricsContextTokensTakeTheLargerValue(t *testing.T) {
	p := &usageProvider{promptTokens: 1, completionTokens: 1}
	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "system", Content: strings.Repeat("context block ", 200)})
	engine := engineWith(p, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	m := oneTurnEnd(t, events).Metrics
	if m == nil {
		t.Fatal("turn end carried no metrics")
	}
	if !m.ProviderReported {
		t.Error("ProviderReported = false despite a usage chunk")
	}
	if m.ContextTokens <= 2 {
		t.Errorf("ContextTokens = %d, want the larger local estimate", m.ContextTokens)
	}
}

// The whole vocabulary in one turn: start first, text before the tool card,
// the result after its start, and end last.
func TestTurnEventOrderAcrossAToolRound(t *testing.T) {
	prov := &metricsProvider{tool: provider.ToolCall{ID: "c1", Name: "metrics_tool", Args: json.RawMessage(`{}`)}}

	toolReg := tools.NewRegistry()
	if err := toolReg.Register(metricsTool{}); err != nil {
		t.Fatalf("register tool: %v", err)
	}
	gate := tools.NewApprovalGate()
	gate.OnPresent = func(string, json.RawMessage, string) (bool, error) { return true, nil }

	reg := provider.NewRegistry()
	reg.Register(prov)
	engine := NewAgentLoop(reg, NewSession("m"), toolReg, gate)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	start := indexOfEventKind(events, EventTurnStart)
	firstDelta := indexOfEventKind(events, EventDelta)
	toolStart := indexOfEventKind(events, EventToolStart)
	toolResult := indexOfEventKind(events, EventToolResult)
	end := indexOfEventKind(events, EventTurnEnd)
	for name, idx := range map[string]int{
		"turn start": start, "delta": firstDelta, "tool start": toolStart,
		"tool result": toolResult, "turn end": end,
	} {
		if idx < 0 {
			t.Fatalf("missing %s event: %v", name, eventKindSequence(events))
		}
	}

	if start != 0 {
		t.Errorf("turn start at %d, want 0", start)
	}
	if !(start < firstDelta && firstDelta < toolStart && toolStart < toolResult && toolResult < end) {
		t.Errorf("events out of order (%v): delta=%d toolStart=%d toolResult=%d end=%d",
			eventKindSequence(events), firstDelta, toolStart, toolResult, end)
	}
	if end != len(events)-1 {
		t.Errorf("turn end at %d of %d, want last", end, len(events))
	}
	oneTurnEnd(t, events)

	m := events[end].Metrics
	if m == nil || m.ToolCalls != 1 {
		t.Errorf("metrics = %+v, want one counted tool call", m)
	}
}

// The plain REPL's turn summary is a one-line contract: same fields, same
// order, no output at all for a command turn or a missing measurement.
func TestTurnSummaryLine(t *testing.T) {
	got := turnSummaryLine(&TurnMetrics{
		PromptTokens:     1234,
		CompletionTokens: 340,
		ToolCalls:        1,
		Elapsed:          2400 * time.Millisecond,
		ContextTokens:    1540,
		ContextBudget:    10000,
	})
	want := "turn: 2.4s · 1.2k prompt / 340 completion tokens · 1 tool call · ctx 15%"
	if got != want {
		t.Errorf("turnSummaryLine = %q, want %q", got, want)
	}
	if strings.ContainsRune(got, '\n') || strings.Count(got, "·") != 3 {
		t.Errorf("summary is not the expected single line: %q", got)
	}

	if got := turnSummaryLine(&TurnMetrics{}); got != "" {
		t.Errorf("empty metrics produced %q, want no line", got)
	}
	if got := turnSummaryLine(nil); got != "" {
		t.Errorf("nil metrics produced %q, want no line", got)
	}

	if got := formatSeconds(95 * time.Second); got != "1m35s" {
		t.Errorf("formatSeconds(95s) = %q, want 1m35s", got)
	}
	if got := formatCount(98765); got != "98.8k" {
		t.Errorf("formatCount(98765) = %q, want 98.8k", got)
	}
}
