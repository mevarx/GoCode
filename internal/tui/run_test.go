package tui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

type blockingProvider struct {
	started chan struct{}
}

func (b *blockingProvider) Name() string { return "blocking" }

func (b *blockingProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (b *blockingProvider) Stream(ctx context.Context, model string, history []provider.Message, toolSpecs []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	if b.started != nil {
		select {
		case <-b.started:
		default:
			close(b.started)
		}
	}
	ch := make(chan provider.StreamChunk)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func newEngine(p provider.Provider, sess *agent.Session) *agent.AgentLoop {
	reg := provider.NewRegistry()
	reg.Register(p)
	return agent.NewAgentLoop(reg, sess, tools.NewRegistry(), tools.NewApprovalGate())
}

func TestInterruptCancelsRunningTurn(t *testing.T) {
	sess := agent.NewSession("m")
	started := make(chan struct{})
	engine := newEngine(&blockingProvider{started: started}, sess)

	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 64)
	cancelCh := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runEngineGoroutine(ctx, engine, inputCh, outputCh, cancelCh)
		close(done)
	}()

	inputCh <- "hello"
	// Wait until Stream runs before interrupting; earlier cancel races HIGH-9 re-drain.
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not start")
	}
	cancelCh <- struct{}{}

	// Drain until done; cancellation may arrive as error event first.
	deadline := time.After(3 * time.Second)
	var doneSeen bool
	for !doneSeen {
		select {
		case msg := <-outputCh:
			switch m := msg.(type) {
			case agentDoneMsg:
				if !errors.Is(m.err, context.Canceled) {
					t.Fatalf("expected context.Canceled after interrupt, got %v", m.err)
				}
				doneSeen = true
			case agentToolMsg:
				// Keep draining error events.
			default:
				t.Fatalf("unexpected message type %T", msg)
			}
		case <-deadline:
			t.Fatal("turn did not complete after interrupt")
		}
	}

	// Goroutine must stay alive for next input.
	select {
	case <-done:
		t.Fatal("engine goroutine exited unexpectedly")
	default:
	}
}

// Observer is wired once in Run; goroutine must not replace it.
func TestEngineGoroutinePreservesObserver(t *testing.T) {
	sess := agent.NewSession("m")
	engine := newEngine(&blockingProvider{}, sess)
	engine.Observe = func(agent.LoopEvent) {}
	original := engine.Observe

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputCh := make(chan string, 1)
	go runEngineGoroutine(ctx, engine, inputCh, make(chan tea.Msg, 8), make(chan struct{}, 1))

	inputCh <- "hi"
	time.Sleep(150 * time.Millisecond)

	if reflect.ValueOf(engine.Observe).Pointer() != reflect.ValueOf(original).Pointer() {
		t.Error("runEngineGoroutine must not replace engine.Observe")
	}
}

// /exit must emit EventExit and report exit request.
func TestExitCommandEmitsExitEvent(t *testing.T) {
	sess := agent.NewSession("m")
	engine := newEngine(&fakeProvider{name: "ollama", models: []string{"llama3"}}, sess)

	var events []agent.LoopEvent
	engine.Observe = func(ev agent.LoopEvent) { events = append(events, ev) }

	err := engine.RunTurn(context.Background(), "/exit")
	if !agent.IsExitRequest(err) {
		t.Fatalf("expected exit request, got %v", err)
	}

	var sawExit bool
	for _, ev := range events {
		if ev.Kind == agent.EventExit {
			sawExit = true
		}
	}
	if !sawExit {
		t.Error("expected an EventExit to be emitted")
	}
}

func TestPlainInputBecomesUserTurn(t *testing.T) {
	sess := agent.NewSession("m")
	engine := newEngine(&fakeProvider{name: "ollama", models: []string{"llama3"}}, sess)

	// fakeProvider can't stream, but message must already be in session.
	_ = engine.RunTurn(context.Background(), "hello there")

	found := false
	for _, m := range sess.History() {
		if m.Role == "user" && m.Content == "hello there" {
			found = true
		}
	}
	if !found {
		t.Error("plain input was not recorded as a user message")
	}
}

type fakeProvider struct {
	name        string
	models      []string
	modelsError error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Models(context.Context) ([]string, error) {
	return f.models, f.modelsError
}

func (f *fakeProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("stream not supported in tests")
}

// Stale parked interrupt must not cancel next message (HIGH-9).
func TestParkedInterruptDoesNotCancelNextTurn(t *testing.T) {
	sess := agent.NewSession("m")
	engine := newEngine(&quickProvider{}, sess)

	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 64)
	cancelCh := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runEngineGoroutine(ctx, engine, inputCh, outputCh, cancelCh)

	// Park stale interrupt first, then submit message.
	cancelCh <- struct{}{}
	inputCh <- "hello"

	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-outputCh:
			if d, ok := msg.(agentDoneMsg); ok {
				if d.err != nil {
					t.Fatalf("next turn was cancelled by a parked interrupt: %v", d.err)
				}
				return
			}
		case <-deadline:
			t.Fatal("turn did not complete")
		}
	}
}

type quickProvider struct{}

func (q *quickProvider) Name() string { return "quick" }
func (q *quickProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (q *quickProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Delta: "hi"}
	ch <- provider.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

// collectPickerItems must bound startup; one stall can't defer list.
func TestCollectPickerItemsBoundsSlowProviders(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&quickProvider{})
	reg.Register(&slowProvider{})

	start := time.Now()
	items := collectPickerItems(context.Background(), reg)
	elapsed := time.Since(start)

	if elapsed > 6*time.Second {
		t.Fatalf("collectPickerItems blocked for %v", elapsed)
	}
	var found bool
	for _, it := range items {
		if mi, ok := it.(ModelItem); ok && mi.Provider == "quick" {
			found = true
		}
	}
	if !found {
		t.Error("expected the fast provider's models in the picker items")
	}
}

type slowProvider struct{}

func (s *slowProvider) Name() string { return "slow" }
func (s *slowProvider) Models(ctx context.Context) ([]string, error) {
	select {
	case <-time.After(30 * time.Second):
		return []string{"x"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *slowProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

// Must discard parked interrupts (HIGH-9) and never block when empty.
func TestDrainCancelCh(t *testing.T) {
	// Empty: returns immediately.
	drainCancelCh(make(chan struct{}, 1))

	// One parked signal: discarded.
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	drainCancelCh(ch)
	select {
	case <-ch:
		t.Fatal("parked interrupt was not drained")
	default:
	}
}

// Must map every event kind so TUI never drops engine output.
func TestToTeaMsgCoversEveryEventKind(t *testing.T) {
	cases := []struct {
		ev   agent.LoopEvent
		want string
	}{
		{agent.LoopEvent{Kind: agent.EventDelta, Text: "hi"}, "agentChunkMsg"},
		{agent.LoopEvent{Kind: agent.EventToolStart, Tool: "t", ToolCallID: "id1", Args: "{}"}, "agentToolStartMsg"},
		{agent.LoopEvent{Kind: agent.EventToolResult, Tool: "t", Text: "out"}, "agentToolMsg"},
		{agent.LoopEvent{Kind: agent.EventError, Text: "bad"}, "agentToolMsg"},
		{agent.LoopEvent{Kind: agent.EventNotice, Text: "note"}, "agentToolMsg"},
		{agent.LoopEvent{Kind: agent.EventExit, Text: "bye"}, "agentExitMsg"},
	}
	for _, tc := range cases {
		msg := toTeaMsg(tc.ev)
		if msg == nil {
			t.Errorf("event kind %v produced nil message", tc.ev.Kind)
			continue
		}
		if got := typeName(msg); got != tc.want {
			t.Errorf("event kind %v mapped to %s, want %s", tc.ev.Kind, got, tc.want)
		}
	}
}

func TestToTeaMsgDropsTurnEnd(t *testing.T) {
	if msg := toTeaMsg(agent.LoopEvent{Kind: agent.EventTurnEnd}); msg != nil {
		t.Errorf("EventTurnEnd should map to nil, got %T", msg)
	}
}

func typeName(v any) string {
	switch v.(type) {
	case agentChunkMsg:
		return "agentChunkMsg"
	case agentToolStartMsg:
		return "agentToolStartMsg"
	case agentToolMsg:
		return "agentToolMsg"
	case agentExitMsg:
		return "agentExitMsg"
	default:
		return "unknown"
	}
}
