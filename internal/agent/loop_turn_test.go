package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// recordingProvider captures streamed history to observe whether the engine truncated.
type recordingProvider struct {
	mu        sync.Mutex
	received  [][]provider.Message
	toolCalls []provider.ToolCall
	text      string
}

func (r *recordingProvider) Name() string { return "recording" }

func (r *recordingProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (r *recordingProvider) Stream(ctx context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	r.mu.Lock()
	// Copy so later mutations of the caller's slice cannot alter the record.
	snapshot := make([]provider.Message, len(history))
	copy(snapshot, history)
	r.received = append(r.received, snapshot)
	toolCalls := r.toolCalls
	text := r.text
	r.mu.Unlock()

	ch := make(chan provider.StreamChunk, 4)
	go func() {
		defer close(ch)
		if text != "" {
			ch <- provider.StreamChunk{Delta: text}
		}
		if len(toolCalls) > 0 {
			ch <- provider.StreamChunk{ToolCalls: toolCalls}
			return
		}
		ch <- provider.StreamChunk{Done: true}
	}()
	return ch, nil
}

func (r *recordingProvider) lastHistory() []provider.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.received) == 0 {
		return nil
	}
	return r.received[len(r.received)-1]
}

func (r *recordingProvider) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.received)
}

func engineWith(p provider.Provider, sess *Session) *AgentLoop {
	reg := provider.NewRegistry()
	reg.Register(p)
	return NewAgentLoop(reg, sess, tools.NewRegistry(), tools.NewApprovalGate())
}

// Regression: the TUI once sent history untruncated while the plain loop truncated.
func TestRunTurnTruncatesHistory(t *testing.T) {
	p := &recordingProvider{text: "done"}
	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "system", Content: "system prompt"})
	for i := 0; i < 40; i++ {
		sess.AddMessage(provider.Message{Role: "user", Content: strings.Repeat("q", 200)})
		sess.AddMessage(provider.Message{Role: "assistant", Content: strings.Repeat("a", 200)})
	}
	full := len(sess.History())

	engine := engineWith(p, sess)
	engine.ContextManager = NewContextManager(0)
	engine.ContextManager.MaxTokens = 300

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	received := p.lastHistory()
	if len(received) >= full {
		t.Fatalf("history was not truncated: sent %d of %d messages", len(received), full)
	}
	if len(received) == 0 {
		t.Fatal("truncation removed everything")
	}
	if received[0].Role != "system" {
		t.Errorf("expected system message first, got %q", received[0].Role)
	}
	if len(received) > 1 && received[1].Role == "tool" {
		t.Error("history starts with an orphaned tool message")
	}
}

func TestRunTurnStopsRunawayToolCalls(t *testing.T) {
	p := &recordingProvider{toolCalls: []provider.ToolCall{{
		ID:   "x",
		Name: "file_read",
		Args: json.RawMessage(`{"path":"missing.txt"}`),
	}}}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	calls := p.callCount()
	if calls == 0 {
		t.Fatal("provider was never called")
	}
	if calls > DefaultMaxRepeatedToolCalls+1 {
		t.Errorf("runaway not stopped: provider called %d times", calls)
	}

	var sawStop bool
	for _, ev := range events {
		if ev.Kind == EventError && strings.Contains(ev.Text, "identical arguments") {
			sawStop = true
		}
	}
	if !sawStop {
		t.Error("expected a visible stop message explaining the repeat-call limit")
	}
}

// A token-limited answer must surface truncation, not store as complete (MEDIUM-16).
func TestTruncatedFinishReasonSurfacesWarning(t *testing.T) {
	sess := NewSession("m")
	engine := engineWith(&truncatingProvider{}, sess)

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	var saw bool
	for _, ev := range events {
		if ev.Kind == EventNotice && strings.Contains(ev.Text, "token limit") {
			saw = true
		}
	}
	if !saw {
		t.Error("expected a notice that the provider stopped at the token limit")
	}
}

type truncatingProvider struct{}

func (t *truncatingProvider) Name() string { return "truncating" }

func (t *truncatingProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (t *truncatingProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Delta: "partial answer"}
	ch <- provider.StreamChunk{Done: true, FinishReason: "length"}
	close(ch)
	return ch, nil
}

func TestRunTurnSurfacesStreamError(t *testing.T) {
	sess := NewSession("m")
	engine := engineWith(&errProvider{}, sess)

	err := engine.RunTurn(context.Background(), "go")
	if err == nil {
		t.Fatal("expected stream error to be returned")
	}
}

type errProvider struct{}

func (e *errProvider) Name() string { return "err" }

func (e *errProvider) Models(context.Context) ([]string, error) { return nil, nil }

func (e *errProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errBoom{}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
