package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// steerProvider queues steering during the streaming of its first round, then
// records the exact history every round received.
type steerProvider struct {
	round     int
	histories [][]provider.Message
	onStream  func(round int)
}

func (p *steerProvider) Name() string { return "steer-script" }

func (p *steerProvider) Models(context.Context) ([]string, error) { return []string{"m"}, nil }

func (p *steerProvider) Stream(_ context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	snapshot := append([]provider.Message(nil), history...)
	p.histories = append(p.histories, snapshot)
	p.round++
	round := p.round
	if p.onStream != nil {
		p.onStream(round)
	}

	ch := make(chan provider.StreamChunk, 8)
	if round == 1 {
		ch <- provider.StreamChunk{Delta: "working"}
		ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{{
			ID: "call_1", Name: "probe", Args: json.RawMessage(`{}`),
		}}}
	} else {
		ch <- provider.StreamChunk{Delta: "done"}
		ch <- provider.StreamChunk{Done: true}
	}
	close(ch)
	return ch, nil
}

func steerEngine(t *testing.T, p provider.Provider) (*AgentLoop, *Session) {
	t.Helper()

	reg := provider.NewRegistry()
	reg.Register(p)
	toolReg := tools.NewRegistry()
	if err := toolReg.Register(&trackedTool{name: "probe", readOnly: true}); err != nil {
		t.Fatalf("register probe: %v", err)
	}
	sess := NewSession("m")
	return NewAgentLoop(reg, sess, toolReg, tools.NewApprovalGate()), sess
}

func hasUserMessage(history []provider.Message, content string) bool {
	for _, msg := range history {
		if msg.Role == "user" && msg.Content == content {
			return true
		}
	}
	return false
}

// Steering entered while round one streams must reach the provider only with
// round two, as a normal user message, and never leak into round one.
func TestSteeringLandsBetweenRounds(t *testing.T) {
	p := &steerProvider{}
	engine, sess := steerEngine(t, p)
	p.onStream = func(round int) {
		if round == 1 {
			if !engine.Steer("focus on tests") {
				t.Error("Steer rejected during a running turn")
			}
		}
	}

	var notices int
	engine.Observe = func(ev LoopEvent) {
		if ev.Kind == EventNotice && ev.Text == SteeringNotice {
			notices++
		}
	}

	if err := engine.RunTurn(context.Background(), "start"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if len(p.histories) != 2 {
		t.Fatalf("expected 2 provider rounds, got %d", len(p.histories))
	}

	steered := steeringMarker + "focus on tests"
	if hasUserMessage(p.histories[0], steered) {
		t.Error("steering was injected into the streaming round")
	}
	if !hasUserMessage(p.histories[1], steered) {
		t.Errorf("round two did not receive the steering message: %+v", p.histories[1])
	}
	if notices != 1 {
		t.Errorf("expected exactly one steering notice, got %d", notices)
	}

	// The steered turn is a user message in the transcript, ordered after the
	// tool result it followed.
	history := sess.History()
	steerIdx, resultIdx := -1, -1
	for i, msg := range history {
		if msg.Role == "user" && msg.Content == steered {
			steerIdx = i
		}
		if msg.Role == "tool" && msg.ToolCallID == "call_1" {
			resultIdx = i
		}
	}
	if steerIdx < 0 || resultIdx < 0 || steerIdx < resultIdx {
		t.Fatalf("steering not ordered after the tool round: steer=%d result=%d", steerIdx, resultIdx)
	}
}

// One drained batch becomes one notice, however many messages it carried.
func TestSteeringDrainEmitsOneNoticePerBatch(t *testing.T) {
	p := &steerProvider{}
	engine, sess := steerEngine(t, p)
	p.onStream = func(round int) {
		if round == 1 {
			engine.Steer("one")
			engine.Steer("two")
		}
	}

	var notices int
	engine.Observe = func(ev LoopEvent) {
		if ev.Kind == EventNotice && ev.Text == SteeringNotice {
			notices++
		}
	}
	if err := engine.RunTurn(context.Background(), "start"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if notices != 1 {
		t.Errorf("expected 1 notice for a 2-message batch, got %d", notices)
	}

	var steered []string
	for _, msg := range sess.History() {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, steeringMarker) {
			steered = append(steered, msg.Content)
		}
	}
	if len(steered) != 2 || steered[0] != steeringMarker+"one" || steered[1] != steeringMarker+"two" {
		t.Errorf("drained steering = %q", steered)
	}
}

// The API is closed outside a turn: no queue, no leaked message, no panic.
func TestSteerRejectedWhenIdle(t *testing.T) {
	p := &steerProvider{}
	engine, sess := steerEngine(t, p)

	if engine.Steer("too early") {
		t.Error("Steer accepted before any turn started")
	}
	if engine.Steer("   ") {
		t.Error("Steer accepted an empty message")
	}
	if err := engine.RunTurn(context.Background(), "start"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if engine.Steer("after the turn") {
		t.Error("Steer accepted after the turn ended")
	}
	for _, msg := range sess.History() {
		if strings.Contains(msg.Content, "too early") || strings.Contains(msg.Content, "after the turn") {
			t.Errorf("rejected steering reached history: %q", msg.Content)
		}
	}
}

// A cancel with steering queued must still leave every declared tool call with
// exactly one result, and must not inject the steer.
func TestSteeringDoesNotBreakAbortPairing(t *testing.T) {
	p := &cancelSteerProvider{}
	engine, sess := steerEngine(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.ToolRegistry.Register(&cancelOnExecuteTool{cancel: cancel})

	p.onStream = func(round int) {
		if round == 1 {
			engine.Steer("never applied")
		}
	}

	if err := engine.RunTurn(ctx, "go"); err == nil {
		t.Fatal("expected the cancelled turn to return an error")
	}
	assertNoDanglingToolCalls(t, sess.History())
	for _, msg := range sess.History() {
		if strings.Contains(msg.Content, "never applied") {
			t.Errorf("steering was injected into an aborted turn: %q", msg.Content)
		}
	}
}

// cancelSteerProvider streams two calls so the cancel lands inside the batch.
type cancelSteerProvider struct {
	round    int
	onStream func(round int)
}

func (p *cancelSteerProvider) Name() string { return "cancel-steer" }

func (p *cancelSteerProvider) Models(context.Context) ([]string, error) { return []string{"m"}, nil }

func (p *cancelSteerProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.round++
	round := p.round
	if p.onStream != nil {
		p.onStream(round)
	}
	ch := make(chan provider.StreamChunk, 4)
	if round == 1 {
		ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{
			{ID: "call_a", Name: "cancel_tool", Args: json.RawMessage(`{}`)},
			{ID: "call_b", Name: "probe", Args: json.RawMessage(`{}`)},
		}}
	} else {
		ch <- provider.StreamChunk{Delta: "done"}
		ch <- provider.StreamChunk{Done: true}
	}
	close(ch)
	return ch, nil
}
