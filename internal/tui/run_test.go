package tui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

type blockingProvider struct{}

func (b *blockingProvider) Name() string { return "blocking" }

func (b *blockingProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (b *blockingProvider) Stream(ctx context.Context, model string, history []provider.Message, toolSpecs []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
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
	engine := newEngine(&blockingProvider{}, sess)

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
	cancelCh <- struct{}{}

	// The turn may report the cancellation as an error event before the done
	// message, so drain until the done message arrives.
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
				// Error events surfaced from the engine; keep draining.
			default:
				t.Fatalf("unexpected message type %T", msg)
			}
		case <-deadline:
			t.Fatal("turn did not complete after interrupt")
		}
	}

	// The goroutine must still be alive and ready for the next input.
	select {
	case <-done:
		t.Fatal("engine goroutine exited unexpectedly")
	default:
	}
}

// The observer is wired once in Run. runEngineGoroutine must not replace it,
// which is the wiring mistake that would silently disconnect the TUI from
// engine output.
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

// /exit must emit EventExit so the program can quit, and report the exit
// request to the caller.
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

// Plain input must be recorded as a user turn, not swallowed as a command.
func TestPlainInputBecomesUserTurn(t *testing.T) {
	sess := agent.NewSession("m")
	engine := newEngine(&fakeProvider{name: "ollama", models: []string{"llama3"}}, sess)

	// fakeProvider cannot stream, so RunTurn errors — but the message must
	// already be in the session.
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

func newTestRegistry(models []string) *provider.Registry {
	r := provider.NewRegistry()
	r.Register(&fakeProvider{name: "ollama", models: models})
	return r
}

// toTeaMsg must map every event kind the engine can emit, so the TUI never
// silently drops engine output.
func TestToTeaMsgCoversEveryEventKind(t *testing.T) {
	cases := []struct {
		ev   agent.LoopEvent
		want string
	}{
		{agent.LoopEvent{Kind: agent.EventDelta, Text: "hi"}, "agentChunkMsg"},
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
	case agentToolMsg:
		return "agentToolMsg"
	case agentExitMsg:
		return "agentExitMsg"
	default:
		return "unknown"
	}
}
