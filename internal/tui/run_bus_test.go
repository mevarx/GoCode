package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/pubsub"
)

// The fan-in pump forwards bus events in order, maps them through toTeaMsg,
// signals the turn-end gate, and exits on ctx cancellation.
func TestPumpBusEventsForwardsInOrderAndSignalsGate(t *testing.T) {
	bus := pubsub.NewBroker[agent.LoopEvent]()
	defer bus.Close()
	events, unsub := bus.Subscribe()
	outputCh := make(chan tea.Msg, 16)
	gate := &turnEndGate{}
	endCh := gate.arm()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		pumpBusEvents(ctx, events, unsub, outputCh, gate)
	}()

	seq := []agent.LoopEvent{
		{Kind: agent.EventTurnStart},
		{Kind: agent.EventDelta, Text: "hi"},
		{Kind: agent.EventToolResult, Tool: "t", Text: "out"},
		{Kind: agent.EventTurnEnd},
	}
	for _, ev := range seq {
		be := pubsub.Event[agent.LoopEvent]{Payload: ev, MustDeliver: ev.Kind == agent.EventTurnEnd}
		if be.MustDeliver {
			bus.PublishMustDeliver(ctx, be)
		} else {
			bus.Publish(be)
		}
	}

	want := []string{"agentTurnStartMsg", "agentChunkMsg", "agentToolMsg", "agentTurnMetricsMsg"}
	for i, w := range want {
		select {
		case msg := <-outputCh:
			if got := typeName(msg); got != w {
				t.Fatalf("position %d: got %s, want %s", i, got, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("pump stalled at position %d", i)
		}
	}
	select {
	case <-endCh:
	case <-time.After(3 * time.Second):
		t.Fatal("turn-end gate never signaled")
	}

	cancel()
	select {
	case <-pumpDone:
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not exit on ctx cancellation")
	}
}

// The v0.7.5 ordering contract through the real wiring: the turn's metrics
// (bus) and chunks (bus) must reach outputCh BEFORE agentDoneMsg (direct),
// because the model flushes the stream buffer on done.
func TestEngineDoneNeverOvertakesBusTurnEnd(t *testing.T) {
	sess := agent.NewSession("m")
	engine := newEngine(&quickProvider{}, sess)
	bus := pubsub.NewBroker[agent.LoopEvent]()
	defer bus.Close()
	engine.Bus = bus
	events, unsub := bus.Subscribe()

	outputCh := make(chan tea.Msg, 64)
	gate := &turnEndGate{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpBusEvents(ctx, events, unsub, outputCh, gate)

	inputCh := make(chan string, 1)
	go runEngineGoroutine(ctx, engine, inputCh, nil, outputCh, make(chan struct{}, 1), gate)
	inputCh <- "hello"

	var order []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-outputCh:
			name := typeName(msg)
			if _, ok := msg.(agentDoneMsg); ok {
				name = "agentDoneMsg"
			}
			order = append(order, name)
			if name == "agentDoneMsg" {
				goto done
			}
		case <-deadline:
			t.Fatalf("turn never completed; order so far: %v", order)
		}
	}
done:
	pos := map[string]int{}
	for i, name := range order {
		if _, dup := pos[name]; !dup {
			pos[name] = i
		}
	}
	for _, early := range []string{"agentTurnStartMsg", "agentChunkMsg", "agentTurnMetricsMsg"} {
		p, ok := pos[early]
		if !ok {
			t.Fatalf("%s missing from output order %v", early, order)
		}
		if p > pos["agentDoneMsg"] {
			t.Fatalf("%s arrived after agentDoneMsg: %v", early, order)
		}
	}
}
