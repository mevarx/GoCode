package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
)

// TUI wiring contracts: mid-turn acceptance queues (never loses) a
// prompt, cancel settles every queued prompt and keeps HIGH-9 stale-signal
// semantics, a cancel-on-entry turn disarms the fan-in gate instead of
// parking on its fallback, and the queued-pills chip reflects pending turns.
// All waits are bounded; fakes only.

// m10Provider blocks its first round until release or ctx cancel and records
// the last user prompt of every round.
type m10Provider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	prompts []string
	rounds  int
}

func (p *m10Provider) Name() string { return "m10-fake" }

func (p *m10Provider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *m10Provider) Stream(ctx context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	p.rounds++
	round := p.rounds
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			p.prompts = append(p.prompts, history[i].Content)
			break
		}
	}
	p.mu.Unlock()

	ch := make(chan provider.StreamChunk)
	go func() {
		defer close(ch)
		if round == 1 {
			p.once.Do(func() { close(p.started) })
			select {
			case <-p.release:
			case <-ctx.Done():
				return
			}
		}
		ch <- provider.StreamChunk{Delta: "ok"}
		ch <- provider.StreamChunk{Done: true, FinishReason: "stop"}
	}()
	return ch, nil
}

func (p *m10Provider) snapshot() (prompts []string, rounds int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...), p.rounds
}

// m10EventLog is a mutex-guarded Observe tap: settlements fire from the
// cancel watcher goroutine while turns run on the executor.
type m10EventLog struct {
	mu     sync.Mutex
	events []agent.LoopEvent
}

func (l *m10EventLog) observe(ev agent.LoopEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *m10EventLog) countEnds(cancelled bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, ev := range l.events {
		if ev.Kind == agent.EventTurnEnd && ev.Cancelled == cancelled {
			n++
		}
	}
	return n
}

func waitM10(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitM10Queued(t *testing.T, engine *agent.AgentLoop, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for engine.QueuedTurnCount() != want {
		if time.Now().After(deadline) {
			t.Fatalf("queued count = %d, want %d", engine.QueuedTurnCount(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// nextDoneMsg pulls the next agentDoneMsg. Error cards are skipped: a
// cancelled turn emits one alongside its done (the v0.7.5 behavior), and
// done.err is the authoritative outcome. A timeout fails the test so a
// regression cannot hide behind a silent channel.
func nextDoneMsg(t *testing.T, outputCh <-chan tea.Msg) agentDoneMsg {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-outputCh:
			if d, ok := msg.(agentDoneMsg); ok {
				return d
			}
		case <-deadline:
			t.Fatal("agentDoneMsg never arrived")
		}
	}
}

// A prompt accepted while a turn runs is queued, not lost: it runs as its
// own turn after the first, with its own done message, in accept order.
func TestEngineQueuesPromptAcceptedMidTurn(t *testing.T) {
	p := &m10Provider{started: make(chan struct{}), release: make(chan struct{})}
	engine := newEngine(p, agent.NewSession("m"))

	inputCh := make(chan string, 4)
	outputCh := make(chan tea.Msg, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runEngineGoroutine(ctx, engine, inputCh, nil, outputCh, make(chan struct{}, 1), nil)

	inputCh <- "one"
	waitM10(t, "first turn to start", p.started)
	inputCh <- "two"
	waitM10Queued(t, engine, 1)
	close(p.release)

	if d := nextDoneMsg(t, outputCh); d.err != nil {
		t.Fatalf("first done = %v, want nil", d.err)
	}
	if d := nextDoneMsg(t, outputCh); d.err != nil {
		t.Fatalf("second done = %v, want nil", d.err)
	}
	prompts, rounds := p.snapshot()
	if rounds != 2 || len(prompts) != 2 || prompts[0] != "one" || prompts[1] != "two" {
		t.Fatalf("rounds=%d prompts=%v, want one then two", rounds, prompts)
	}
	if engine.QueuedTurnCount() != 0 {
		t.Errorf("queue not drained: %d", engine.QueuedTurnCount())
	}
}

// Cancel settles every queued prompt with a terminal cancelled event, ends
// the active turn cooperatively, and leaves the loop ready for the next
// prompt (HIGH-9: the consumed interrupt never cancels a later turn).
func TestEngineCancelSettlesQueuedPrompts(t *testing.T) {
	p := &m10Provider{started: make(chan struct{}), release: make(chan struct{})}
	engine := newEngine(p, agent.NewSession("m"))
	log := &m10EventLog{}
	engine.Observe = log.observe

	inputCh := make(chan string, 4)
	outputCh := make(chan tea.Msg, 64)
	cancelCh := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runEngineGoroutine(ctx, engine, inputCh, nil, outputCh, cancelCh, nil)

	inputCh <- "one"
	waitM10(t, "first turn to start", p.started)
	inputCh <- "two"
	inputCh <- "three"
	waitM10Queued(t, engine, 2)

	cancelCh <- struct{}{}
	if d := nextDoneMsg(t, outputCh); !errors.Is(d.err, context.Canceled) {
		t.Fatalf("done after cancel = %v, want context.Canceled", d.err)
	}

	// Settlements fire from the watcher; wait for both terminal events.
	deadline := time.Now().Add(3 * time.Second)
	for log.countEnds(true) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("cancelled settlements = %d, want 2", log.countEnds(true))
		}
		time.Sleep(time.Millisecond)
	}
	if got := log.countEnds(false); got != 1 {
		t.Errorf("primary turn ends = %d, want exactly 1", got)
	}
	if engine.QueuedTurnCount() != 0 {
		t.Errorf("queue survived cancel: %d", engine.QueuedTurnCount())
	}

	// The next prompt runs normally: the interrupt was consumed, not parked.
	inputCh <- "four"
	if d := nextDoneMsg(t, outputCh); d.err != nil {
		t.Fatalf("post-cancel turn = %v, want nil (HIGH-9)", d.err)
	}
	if _, rounds := p.snapshot(); rounds != 2 {
		t.Errorf("provider rounds = %d, want 2 (cancelled queue never ran)", rounds)
	}
}

// A cancel-on-entry turn (accepted before the mark, entered after) settles
// without running and DISARMS the fan-in gate: the executor must not park on
// the 1s fallback for a turn that emits no primary end event.
func TestEngineCancelOnEntryDisarmsGate(t *testing.T) {
	engine := newEngine(&quickProvider{}, agent.NewSession("m"))
	log := &m10EventLog{}
	engine.Observe = log.observe
	gate := &turnEndGate{}

	// Accept, cancel, then enqueue: only the entry mark-test can settle this.
	seq := engine.BeginAccepted()
	engine.CancelTurns()
	engine.EnqueueTurn(seq, "covered")

	outputCh := make(chan tea.Msg, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runEngineGoroutine(ctx, engine, make(chan string, 1), nil, outputCh, make(chan struct{}, 1), gate)

	if d := nextDoneMsg(t, outputCh); d.err != nil {
		t.Fatalf("settled turn done = %v, want nil", d.err)
	}
	if got := log.countEnds(true); got != 1 {
		t.Fatalf("cancelled settlements = %d, want 1", got)
	}
	// The gate must be disarmed, not left armed for the fallback timeout.
	gate.mu.Lock()
	armed := gate.ch != nil
	gate.mu.Unlock()
	if armed {
		t.Error("gate left armed after a cancel-on-entry turn")
	}
}

// The queued-pills chip reflects pending dispatcher turns alongside steering.
func TestQueuedPillsReflectPendingTurns(t *testing.T) {
	m := newTestModel(nil)
	engine := newEngine(&quickProvider{}, agent.NewSession("m"))
	m.engine = engine

	if chip := m.renderSteerChip(); chip != "" {
		t.Fatalf("idle chip = %q, want empty", chip)
	}
	engine.AcceptTurn("a")
	chip := m.renderSteerChip()
	if !strings.Contains(chip, "1 turn queued") || !strings.Contains(chip, "runs after this turn") {
		t.Errorf("chip = %q, want one queued turn", chip)
	}
	engine.AcceptTurn("b")
	if chip := m.renderSteerChip(); !strings.Contains(chip, "2 turns queued") {
		t.Errorf("chip = %q, want two queued turns", chip)
	}
	m.queuedSteer = 1
	chip = m.renderSteerChip()
	if !strings.Contains(chip, "1 steered message queued") || !strings.Contains(chip, "2 turns queued") {
		t.Errorf("chip = %q, want steering AND queued turns", chip)
	}
	// Draining the queue empties the turn part; model-only (nil engine) stays
	// on the legacy steering-only contract.
	_, _, _ = engine.NextQueuedTurn()
	_, _, _ = engine.NextQueuedTurn()
	m.queuedSteer = 0
	if chip := m.renderSteerChip(); chip != "" {
		t.Errorf("chip after drain = %q, want empty", chip)
	}
	m.engine = nil
	m.queuedSteer = 1
	if chip := m.renderSteerChip(); !strings.Contains(chip, "1 steered message queued") {
		t.Errorf("nil-engine chip = %q, want the steering-only label", chip)
	}
}

// A settled queued turn renders its terminal state in the transcript.
func TestSettledTurnRendersStoppedLine(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = drive(m, agentTurnMetricsMsg{cancelled: true})

	var found bool
	for _, msg := range m.messages {
		if strings.Contains(msg.Content, "queued turn cancelled before it started") {
			found = true
		}
	}
	if !found {
		t.Error("settlement did not render a terminal transcript line")
	}
}
