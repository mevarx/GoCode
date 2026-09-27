package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// recordingProvider captures the history it was asked to stream, which is the
// only way to observe whether runTurn truncated before sending.
type recordingProvider struct {
	mu       sync.Mutex
	received [][]provider.Message
	// toolCalls, when set, is emitted on every round-trip.
	toolCalls []provider.ToolCall
	// text, when set, is emitted before the tool calls.
	text string
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

// TestRunTurnTruncatesHistory is the regression test for the defect where the
// TUI sent session.History() untruncated while the plain loop called
// ContextManager.Truncate. With a tiny MaxTokens, a long history must arrive at
// the provider already shortened.
func TestRunTurnTruncatesHistory(t *testing.T) {
	// Reply with text only, so the turn ends after one round-trip.
	p := &recordingProvider{text: "done"}

	reg := provider.NewRegistry()
	reg.Register(p)

	sess := agent.NewSession("m")
	// A long history: one system message plus many user/assistant turns.
	sess.AddMessage(provider.Message{Role: "system", Content: "system prompt"})
	for i := 0; i < 40; i++ {
		sess.AddMessage(provider.Message{Role: "user", Content: strings.Repeat("q", 200)})
		sess.AddMessage(provider.Message{Role: "assistant", Content: strings.Repeat("a", 200)})
	}
	full := len(sess.History())

	out := make(chan tea.Msg, 256)
	cm := agent.NewContextManager(0)
	cm.MaxTokens = 300 // far below the full history

	if err := runTurn(context.Background(), reg, sess, tools.NewRegistry(),
		tools.NewApprovalGate(), nil, out, cm); err != nil {
		t.Fatalf("runTurn returned error: %v", err)
	}

	received := p.lastHistory()
	if len(received) >= full {
		t.Fatalf("history was not truncated: sent %d of %d messages", len(received), full)
	}
	if len(received) == 0 {
		t.Fatal("truncation removed everything")
	}
	// The system message must survive truncation.
	if received[0].Role != "system" {
		t.Errorf("expected system message first, got %q", received[0].Role)
	}
	// No orphaned tool message may lead the history.
	if len(received) > 1 && received[1].Role == "tool" {
		t.Errorf("history starts with an orphaned tool message")
	}
}

// The guard must stop a runaway turn in the TUI path exactly as it does in the
// plain path, and must report it to the user.
func TestRunTurnStopsRunawayToolCalls(t *testing.T) {
	p := &recordingProvider{toolCalls: []provider.ToolCall{{
		ID:   "x",
		Name: "file_read",
		Args: json.RawMessage(`{"path":"missing.txt"}`),
	}}}

	reg := provider.NewRegistry()
	reg.Register(p)
	sess := agent.NewSession("m")
	sess.AddMessage(provider.Message{Role: "user", Content: "go"})

	out := make(chan tea.Msg, 256)
	// A tool registry without the tool: every call errors, which is the
	// runaway shape — the model retries the same call forever.
	if err := runTurn(context.Background(), reg, sess, tools.NewRegistry(),
		tools.NewApprovalGate(), nil, out, agent.NewContextManager(0)); err != nil {
		t.Fatalf("runTurn returned error: %v", err)
	}

	calls := p.callCount()
	if calls == 0 {
		t.Fatal("provider was never called")
	}
	if calls > agent.DefaultMaxRepeatedToolCalls+1 {
		t.Errorf("runaway not stopped: provider called %d times", calls)
	}

	// The stop must be visible to the user.
	var sawStop bool
	for len(out) > 0 {
		if msg, ok := (<-out).(agentToolMsg); ok {
			if msg.isError && strings.Contains(msg.result, "identical arguments") {
				sawStop = true
			}
		}
	}
	if !sawStop {
		t.Error("expected a visible stop message explaining the repeat-call limit")
	}
}

// A nil ContextManager must not panic; runTurn falls back to a default.
func TestRunTurnTolerantOfNilContextManager(t *testing.T) {
	p := &recordingProvider{text: "ok"}
	reg := provider.NewRegistry()
	reg.Register(p)
	sess := agent.NewSession("m")
	sess.AddMessage(provider.Message{Role: "user", Content: "go"})

	out := make(chan tea.Msg, 16)
	err := runTurn(context.Background(), reg, sess, tools.NewRegistry(),
		tools.NewApprovalGate(), nil, out, nil)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A provider that cannot stream must surface the error, not spin.
func TestRunTurnSurfacesStreamError(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&errProvider{})
	sess := agent.NewSession("m")
	sess.AddMessage(provider.Message{Role: "user", Content: "go"})

	out := make(chan tea.Msg, 16)
	err := runTurn(context.Background(), reg, sess, tools.NewRegistry(),
		tools.NewApprovalGate(), nil, out, agent.NewContextManager(0))
	if err == nil {
		t.Fatal("expected stream error to be returned")
	}
}

type errProvider struct{}

func (e *errProvider) Name() string { return "err" }

func (e *errProvider) Models(context.Context) ([]string, error) { return nil, nil }

func (e *errProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("boom")
}
