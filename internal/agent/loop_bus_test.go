package agent

import (
	"context"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/pubsub"
)

func TestBusEventClassification(t *testing.T) {
	cases := []struct {
		ev       LoopEvent
		wantType string
		wantKey  string
		wantMust bool
	}{
		{LoopEvent{Kind: EventTurnStart}, pubsub.TypeAgentEvent, "", false},
		{LoopEvent{Kind: EventDelta, Text: "hi"}, pubsub.TypeMessage, "", false},
		{LoopEvent{Kind: EventToolStart, Tool: "t", ToolCallID: "id1"}, pubsub.TypeAgentEvent, "", false},
		{LoopEvent{Kind: EventToolDelta, Tool: "t", ToolCallID: "id1"}, pubsub.TypeMessage, "", false},
		{LoopEvent{Kind: EventToolResult, Tool: "t", ToolCallID: "id1"}, pubsub.TypeAgentEvent, "", true},
		{LoopEvent{Kind: EventNotice, Text: "n"}, pubsub.TypeAgentEvent, "", false},
		{LoopEvent{Kind: EventError, Text: "e"}, pubsub.TypeAgentEvent, "", true},
		{LoopEvent{Kind: EventExit}, pubsub.TypeAgentEvent, "", true},
		{LoopEvent{Kind: EventTurnEnd}, pubsub.TypeRunComplete, "", true},
	}
	for _, tc := range cases {
		got := busEventFor(tc.ev)
		if got.Type != tc.wantType || got.Key != tc.wantKey || got.MustDeliver != tc.wantMust {
			t.Errorf("kind %v: got {Type:%q Key:%q Must:%v}, want {Type:%q Key:%q Must:%v}",
				tc.ev.Kind, got.Type, got.Key, got.MustDeliver, tc.wantType, tc.wantKey, tc.wantMust)
		}
		if got.Payload.Kind != tc.ev.Kind {
			t.Errorf("kind %v: payload not carried through", tc.ev.Kind)
		}
	}
}

// busRecorder subscribes before the turn and collects until the terminal
// event arrives, so the comparison never races the pump.
type busRecorder struct {
	events []LoopEvent
	done   chan struct{}
}

func recordBus(t *testing.T, b *pubsub.Broker[LoopEvent]) *busRecorder {
	t.Helper()
	ch, unsub := b.Subscribe()
	t.Cleanup(unsub)
	r := &busRecorder{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for ev := range ch {
			r.events = append(r.events, ev.Payload)
			if ev.Payload.Kind == EventTurnEnd {
				return
			}
		}
	}()
	return r
}

func (r *busRecorder) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("bus never delivered the turn end")
	}
}

// A single-round turn with every event kind: the bus sequence must be
// IDENTICAL to the direct Observe recording (the v0.7.5 ordering contract).
func TestBusSequenceMatchesObserveExactly(t *testing.T) {
	p := &busOneShotProvider{chunks: []provider.StreamChunk{
		{Delta: "hi"},
		{ToolCallDelta: &provider.ToolCallDelta{ID: "c1", Name: "file_read", ArgsDelta: `{"path":"a"}`}},
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "file_read", Args: []byte(`{"path":"a"}`)}}},
		{Done: true},
	}}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	var observed []LoopEvent
	engine.Observe = func(ev LoopEvent) { observed = append(observed, ev) }
	engine.Bus = pubsub.NewBroker[LoopEvent]()
	defer engine.Bus.Close()
	rec := recordBus(t, engine.Bus)

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	rec.wait(t)

	if len(rec.events) != len(observed) {
		t.Fatalf("bus delivered %d events, Observe saw %d", len(rec.events), len(observed))
	}
	for i, ev := range observed {
		got := rec.events[i]
		if got.Kind != ev.Kind || got.Text != ev.Text || got.ToolCallID != ev.ToolCallID || got.Tool != ev.Tool {
			t.Fatalf("event %d differs: bus %+v, observe %+v", i, got, ev)
		}
	}
}

// Many deltas in one turn: with frame-fidelity delivery (no coalescing keys
// on loop deltas) the bus sequence must match the Observe recording exactly,
// and the turn end must arrive exactly once.
func TestBusPreservesFullSequenceWithManyDeltas(t *testing.T) {
	var chunks []provider.StreamChunk
	for i := 0; i < 50; i++ {
		chunks = append(chunks, provider.StreamChunk{Delta: "tok"})
	}
	chunks = append(chunks,
		provider.StreamChunk{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "unknown_tool"}}},
		provider.StreamChunk{Done: true},
	)
	sess := NewSession("m")
	engine := engineWith(&scriptedProvider{chunks: chunks}, sess)

	var observed []LoopEvent
	engine.Observe = func(ev LoopEvent) { observed = append(observed, ev) }
	engine.Bus = pubsub.NewBroker[LoopEvent]()
	defer engine.Bus.Close()
	rec := recordBus(t, engine.Bus)

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	rec.wait(t)

	if len(rec.events) != len(observed) {
		t.Fatalf("bus delivered %d events, Observe saw %d — frame stream must be lossless at this volume", len(rec.events), len(observed))
	}
	for i, ev := range observed {
		got := rec.events[i]
		if got.Kind != ev.Kind || got.Text != ev.Text || got.ToolCallID != ev.ToolCallID {
			t.Fatalf("event %d differs: bus %+v, observe %+v", i, got, ev)
		}
	}
	ends := 0
	for _, ev := range rec.events {
		if ev.Kind == EventTurnEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("turn end delivered %d times, want exactly 1", ends)
	}
	if rec.events[0].Kind != EventTurnStart || rec.events[len(rec.events)-1].Kind != EventTurnEnd {
		t.Fatal("bus sequence must start with TurnStart and end with TurnEnd")
	}
}

// A cancelled turn still ends exactly once on the bus (P1 funnel shared).
func TestBusTurnEndExactlyOnceOnCancel(t *testing.T) {
	sess := NewSession("m")
	engine := engineWith(&busBlockingProvider{}, sess)
	engine.Bus = pubsub.NewBroker[LoopEvent]()
	defer engine.Bus.Close()
	rec := recordBus(t, engine.Bus)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := engine.RunTurn(ctx, "go")
	if err == nil {
		t.Fatal("cancelled turn must report an error")
	}
	rec.wait(t)

	ends := 0
	for _, ev := range rec.events {
		if ev.Kind == EventTurnEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("turn end delivered %d times on cancel, want exactly 1", ends)
	}
}

// Nil bus is the plain-loop/test path: emit must not panic or publish.
func TestBusNilSafe(t *testing.T) {
	sess := NewSession("m")
	engine := engineWith(&scriptedProvider{chunks: []provider.StreamChunk{{Delta: "x"}, {Done: true}}}, sess)
	var seen int
	engine.Observe = func(LoopEvent) { seen++ }
	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn without bus: %v", err)
	}
	if seen == 0 {
		t.Fatal("Observe must still fire without a bus")
	}
}

// busBlockingProvider streams nothing until the context dies.
type busBlockingProvider struct{}

func (busBlockingProvider) Name() string { return "bus-blocking" }
func (busBlockingProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}
func (busBlockingProvider) Stream(ctx context.Context, _ string, _ []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// busOneShotProvider replays its script once; later rounds end immediately.
type busOneShotProvider struct {
	chunks []provider.StreamChunk
	used   bool
}

func (p *busOneShotProvider) Name() string { return "bus-one-shot" }
func (p *busOneShotProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}
func (p *busOneShotProvider) Stream(_ context.Context, _ string, _ []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	chunks := []provider.StreamChunk{{Done: true}}
	if !p.used {
		p.used = true
		chunks = p.chunks
	}
	ch := make(chan provider.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}
