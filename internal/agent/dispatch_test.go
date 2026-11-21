package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
)

// Contract tests for the dispatch state machine: the three atomic outcomes, accept-sequence marks, FIFO queue
// order, the cancel-on-entry race, queue settlement, cooperative cancel and
// preserved steering. Every wait is bounded so a regression fails fast
// instead of hanging the suite. Fakes only — no network, Windows-safe.

// dispatchProvider blocks its first round until release (or ctx cancel) and
// records the last user prompt every round received, so queue order and
// steering are observable. Optional delta/toolCall chunks stream before the
// block to exercise cooperative-cancel retention.
type dispatchProvider struct {
	started  chan struct{}
	release  chan struct{}
	delta    string
	toolCall *provider.ToolCall
	once     sync.Once

	mu      sync.Mutex
	prompts []string
	rounds  int
}

func newDispatchProvider() *dispatchProvider {
	return &dispatchProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *dispatchProvider) Name() string { return "dispatch-fake" }

func (p *dispatchProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *dispatchProvider) Stream(ctx context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
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
			if p.delta != "" {
				ch <- provider.StreamChunk{Delta: p.delta}
			}
			if p.toolCall != nil {
				ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{*p.toolCall}}
			}
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

func (p *dispatchProvider) snapshot() (prompts []string, rounds int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...), p.rounds
}

// dispatchLog collects events under a mutex: settlement can fire from a
// cancel goroutine while a turn runs on another.
type dispatchLog struct {
	mu     sync.Mutex
	events []LoopEvent
}

func (l *dispatchLog) observe(ev LoopEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *dispatchLog) snapshot() []LoopEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LoopEvent(nil), l.events...)
}

func (l *dispatchLog) countKind(kind LoopEventKind) int {
	return countEventKind(l.snapshot(), kind)
}

func (l *dispatchLog) countCancelledEnds() int {
	n := 0
	for _, ev := range l.snapshot() {
		if ev.Kind == EventTurnEnd && ev.Cancelled {
			n++
		}
	}
	return n
}

func waitDispatchSignal(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// waitQueuedCount polls until the dispatcher holds want pending prompts.
func waitQueuedCount(t *testing.T, engine *AgentLoop, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for engine.QueuedTurnCount() != want {
		if time.Now().After(deadline) {
			t.Fatalf("queued count = %d, want %d", engine.QueuedTurnCount(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// mustQueue runs RunTurn from a separate goroutine and requires it to return
// promptly with nil: the queued outcome must never block on the active turn.
func mustQueue(t *testing.T, engine *AgentLoop, text string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- engine.RunTurn(context.Background(), text) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("queued RunTurn(%q) returned %v, want nil", text, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunTurn(%q) blocked instead of queueing", text)
	}
}

func hasAssistantMessage(history []provider.Message, content string) bool {
	for _, msg := range history {
		if msg.Role == "assistant" && msg.Content == content {
			return true
		}
	}
	return false
}

// Outcome 3 (active): an idle session admits the call, runs it, and closes it
// exactly once with metrics. The accept counter advanced; the mark did not.
func TestDispatchActiveOutcome(t *testing.T) {
	p := newDispatchProvider()
	close(p.release)
	engine := engineWith(p, NewSession("m"))
	log := &dispatchLog{}
	engine.Observe = log.observe

	seq := engine.BeginAccepted()
	if seq != 1 || engine.AcceptSequence() != 1 {
		t.Fatalf("accept sequence = %d/%d, want 1/1", seq, engine.AcceptSequence())
	}
	ran, err := engine.RunTurnAccepted(context.Background(), "go", seq)
	if err != nil || !ran {
		t.Fatalf("RunTurnAccepted = (%v, %v), want (true, nil)", ran, err)
	}
	if got := log.countKind(EventTurnStart); got != 1 {
		t.Errorf("TurnStart count = %d, want 1", got)
	}
	end := oneTurnEnd(t, log.snapshot())
	if end.Cancelled || end.Err != nil || end.Metrics == nil {
		t.Errorf("active turn end = %+v, want a clean measured end", end)
	}
	if engine.CancelMark() != 0 || engine.QueuedTurnCount() != 0 {
		t.Errorf("mark/queue = %d/%d, want 0/0", engine.CancelMark(), engine.QueuedTurnCount())
	}
	if _, rounds := p.snapshot(); rounds != 1 {
		t.Errorf("provider rounds = %d, want 1", rounds)
	}
}

// Outcome 2 (queued) + FIFO: prompts accepted while a turn runs return
// immediately and execute in accept order once the active turn finishes.
func TestDispatchQueuedOutcomeAndFIFOOrder(t *testing.T) {
	p := newDispatchProvider()
	engine := engineWith(p, NewSession("m"))
	log := &dispatchLog{}
	engine.Observe = log.observe

	firstDone := make(chan error, 1)
	go func() { firstDone <- engine.RunTurn(context.Background(), "one") }()
	waitDispatchSignal(t, "first turn to start", p.started)

	// A previously accepted handle enters as queued and returns at once.
	seq2 := engine.BeginAccepted()
	type outcome struct {
		ran bool
		err error
	}
	second := make(chan outcome, 1)
	go func() {
		ran, err := engine.RunTurnAccepted(context.Background(), "two", seq2)
		second <- outcome{ran, err}
	}()
	select {
	case o := <-second:
		if o.ran || o.err != nil {
			t.Fatalf("queued entry = (%v, %v), want (false, nil)", o.ran, o.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued RunTurnAccepted blocked instead of returning immediately")
	}
	// The self-draining entry queues too when a turn is active.
	mustQueue(t, engine, "three")
	waitQueuedCount(t, engine, 2)

	close(p.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first turn never finished")
	}

	prompts, rounds := p.snapshot()
	if rounds != 3 {
		t.Fatalf("provider rounds = %d, want 3 (one per queued turn)", rounds)
	}
	want := []string{"one", "two", "three"}
	for i, w := range want {
		if i >= len(prompts) || prompts[i] != w {
			t.Fatalf("prompt order = %v, want %v (FIFO)", prompts, want)
		}
	}
	if got := log.countKind(EventTurnStart); got != 3 {
		t.Errorf("TurnStart count = %d, want 3", got)
	}
	if got := log.countKind(EventTurnEnd); got != 3 {
		t.Errorf("TurnEnd count = %d, want exactly one per turn (3)", got)
	}
	if got := log.countCancelledEnds(); got != 0 {
		t.Errorf("cancelled ends = %d, want 0", got)
	}
	if engine.QueuedTurnCount() != 0 {
		t.Errorf("queue not drained: %d", engine.QueuedTurnCount())
	}
}

// Outcome 1 (cancel-on-entry): a call whose sequence is covered by the mark
// never enters the stream; its cancelled row is persisted and exactly one
// terminal cancelled event is emitted.
func TestDispatchCancelOnEntry(t *testing.T) {
	p := newDispatchProvider()
	sess := NewSession("m")
	engine := engineWith(p, sess)
	log := &dispatchLog{}
	engine.Observe = log.observe

	seq := engine.BeginAccepted()
	engine.CancelTurns()
	ran, err := engine.RunTurnAccepted(context.Background(), "never runs", seq)
	if ran || err != nil {
		t.Fatalf("cancel-on-entry = (%v, %v), want (false, nil)", ran, err)
	}
	if _, rounds := p.snapshot(); rounds != 0 {
		t.Fatalf("provider ran %d rounds for a cancel-on-entry call", rounds)
	}
	if !hasUserMessage(sess.History(), "never runs") {
		t.Error("cancelled turn's user row was not persisted")
	}
	if !hasAssistantMessage(sess.History(), cancelledTurnNotice) {
		t.Error("cancelled turn row was not persisted")
	}
	if got := log.countKind(EventTurnStart); got != 0 {
		t.Errorf("cancel-on-entry emitted %d TurnStart events, want 0", got)
	}
	end := oneTurnEnd(t, log.snapshot())
	if !end.Cancelled || !errors.Is(end.Err, context.Canceled) {
		t.Errorf("settlement end = %+v, want Cancelled with context.Canceled", end)
	}
}

// Sequence marks: BeginAccepted is monotonic, Cancel raises the mark to the
// current accept sequence, covered calls settle and later calls run. A cancel
// while idle never poisons future turns.
func TestDispatchSequenceMarks(t *testing.T) {
	p := newDispatchProvider()
	close(p.release)
	engine := engineWith(p, NewSession("m"))
	log := &dispatchLog{}
	engine.Observe = log.observe

	s1 := engine.BeginAccepted()
	s2 := engine.BeginAccepted()
	if s2 != s1+1 {
		t.Fatalf("accept sequences not monotonic: %d then %d", s1, s2)
	}
	engine.CancelTurns()
	if got := engine.CancelMark(); got != s2 {
		t.Fatalf("cancel mark = %d, want the current accept sequence %d", got, s2)
	}
	if ran, _ := engine.RunTurnAccepted(context.Background(), "covered", s1); ran {
		t.Error("a call at or below the mark ran instead of settling")
	}

	s3 := engine.BeginAccepted()
	if s3 <= engine.CancelMark() {
		t.Fatalf("new accept %d must exceed the mark %d", s3, engine.CancelMark())
	}
	if ran, err := engine.RunTurnAccepted(context.Background(), "fresh", s3); !ran || err != nil {
		t.Fatalf("post-cancel call = (%v, %v), want (true, nil)", ran, err)
	}

	// Idle cancel, then a plain RunTurn: still runs.
	engine.CancelTurns()
	if err := engine.RunTurn(context.Background(), "after idle cancel"); err != nil {
		t.Fatalf("RunTurn after idle cancel: %v", err)
	}
	if got := log.countCancelledEnds(); got != 1 {
		t.Errorf("cancelled ends = %d, want only the covered call (1)", got)
	}
}

// The cancel-on-entry race: a Cancel landing between BeginAccepted and turn
// entry settles the call — no hang, one terminal event, no stream. The stress
// subtest runs the interleaving concurrently for the race detector.
func TestDispatchCancelOnEntryRace(t *testing.T) {
	t.Run("deterministic-window", func(t *testing.T) {
		p := newDispatchProvider()
		engine := engineWith(p, NewSession("m"))
		log := &dispatchLog{}
		engine.Observe = log.observe

		seq := engine.BeginAccepted()
		// Cancel lands after accept, before entry.
		engine.CancelTurns()

		entered := make(chan bool, 1)
		go func() {
			ran, err := engine.RunTurnAccepted(context.Background(), "raced", seq)
			if err != nil {
				t.Errorf("raced entry returned %v", err)
			}
			entered <- ran
		}()
		select {
		case ran := <-entered:
			if ran {
				t.Fatal("the cancel window was lost: the call ran")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel-on-entry hung the caller")
		}
		end := oneTurnEnd(t, log.snapshot())
		if !end.Cancelled {
			t.Errorf("race settlement end = %+v, want Cancelled", end)
		}
		if _, rounds := p.snapshot(); rounds != 0 {
			t.Errorf("provider ran %d rounds for a settled call", rounds)
		}
	})

	t.Run("concurrent-stress", func(t *testing.T) {
		engine := engineWith(&scriptedProvider{chunks: []provider.StreamChunk{
			{Delta: "ok"}, {Done: true, FinishReason: "stop"},
		}}, NewSession("m"))
		log := &dispatchLog{}
		engine.Observe = log.observe

		const calls = 32
		for i := 0; i < calls; i++ {
			seq := engine.BeginAccepted()
			done := make(chan struct{})
			go func() {
				defer close(done)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					_, _ = engine.RunTurnAccepted(context.Background(), fmt.Sprintf("p%d", i), seq)
				}()
				go func() {
					defer wg.Done()
					engine.CancelTurns()
				}()
				wg.Wait()
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("iteration %d hung: a call was neither run nor settled", i)
			}
		}
		// Every accepted call ends exactly once — run or settled, never both,
		// never neither.
		if got := log.countKind(EventTurnEnd); got != calls {
			t.Fatalf("terminal events = %d, want exactly one per accepted call (%d)", got, calls)
		}
	})
}

// Cancel clears the queue and settles EVERY dropped prompt with a terminal
// cancelled event, in queue order; the active turn winds down cooperatively
// and keeps its partial output. No caller is left hanging.
func TestCancelClearsQueueSettlingEveryPrompt(t *testing.T) {
	p := newDispatchProvider()
	p.delta = "partial"
	sess := NewSession("m")
	engine := engineWith(p, sess)
	log := &dispatchLog{}
	engine.Observe = log.observe

	firstDone := make(chan error, 1)
	go func() { firstDone <- engine.RunTurn(context.Background(), "one") }()
	waitDispatchSignal(t, "first turn to start", p.started)
	mustQueue(t, engine, "two")
	mustQueue(t, engine, "three")
	waitQueuedCount(t, engine, 2)

	settled := engine.CancelTurns()
	if len(settled) != 2 {
		t.Fatalf("settled %d calls, want every queued prompt (2)", len(settled))
	}
	if settled[0].Text != "two" || settled[1].Text != "three" {
		t.Errorf("settlement order = %v, want FIFO [two three]", settled)
	}
	if settled[0].Seq >= settled[1].Seq {
		t.Errorf("settled sequences not monotonic: %v", settled)
	}

	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active turn after Cancel = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel left the active turn hanging")
	}

	if got := log.countCancelledEnds(); got != 2 {
		t.Errorf("cancelled terminal events = %d, want one per dropped prompt (2)", got)
	}
	ends := log.countKind(EventTurnEnd)
	if ends != 3 {
		t.Errorf("total terminal events = %d, want 3 (2 settlements + 1 active)", ends)
	}
	for _, ev := range log.snapshot() {
		if ev.Kind == EventTurnEnd && !ev.Cancelled {
			if !errors.Is(ev.Err, context.Canceled) || ev.Metrics == nil {
				t.Errorf("active end = %+v, want Canceled with metrics", ev)
			}
		}
	}
	if !hasAssistantMessage(sess.History(), "partial") {
		t.Error("cooperative cancel dropped the partial assistant message")
	}
	for _, text := range []string{"two", "three"} {
		if !hasUserMessage(sess.History(), text) {
			t.Errorf("settled prompt %q was not persisted", text)
		}
	}
	if engine.QueuedTurnCount() != 0 {
		t.Errorf("queue survived Cancel: %d", engine.QueuedTurnCount())
	}
	if _, rounds := p.snapshot(); rounds != 1 {
		t.Errorf("provider rounds = %d, want 1 (queued turns never ran)", rounds)
	}
}

// Cooperative cancel keeps every declared tool call paired with a synthetic
// interrupted result, so the next request is never rejected.
func TestCancelKeepsToolCallPairing(t *testing.T) {
	p := newDispatchProvider()
	p.toolCall = &provider.ToolCall{ID: "c1", Name: "probe", Args: json.RawMessage(`{}`)}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	done := make(chan error, 1)
	go func() { done <- engine.RunTurn(context.Background(), "go") }()
	waitDispatchSignal(t, "turn to start", p.started)
	engine.CancelTurns()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled turn = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled turn hung")
	}
	assertNoDanglingToolCalls(t, sess.History())
	var found bool
	for _, msg := range sess.History() {
		if msg.Role == "tool" && msg.ToolCallID == "c1" && strings.Contains(msg.Content, "interrupted") {
			found = true
		}
	}
	if !found {
		t.Error("the unreached call has no interrupted result")
	}
}

// Steering keeps its own lane: mid-turn guidance folds into the running turn
// at the round boundary, while a queued prompt runs as its OWN later turn.
func TestSteeringPreservedWithQueuedTurn(t *testing.T) {
	p := &steerProvider{}
	engine, sess := steerEngine(t, p)
	log := &dispatchLog{}
	engine.Observe = log.observe

	nextDone := make(chan error, 1)
	p.onStream = func(round int) {
		if round == 1 {
			if !engine.Steer("focus") {
				t.Error("Steer rejected during a running turn")
			}
			go func() { nextDone <- engine.RunTurn(context.Background(), "next turn") }()
		}
	}

	if err := engine.RunTurn(context.Background(), "start"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	select {
	case err := <-nextDone:
		if err != nil {
			t.Fatalf("queued turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued turn never ran")
	}

	if len(p.histories) != 3 {
		t.Fatalf("provider rounds = %d, want 3 (steered turn + queued turn)", len(p.histories))
	}
	steered := steeringMarker + "focus"
	if hasUserMessage(p.histories[0], steered) {
		t.Error("steering leaked into the streaming round")
	}
	if !hasUserMessage(p.histories[1], steered) {
		t.Error("steering did not fold into the running turn's next round")
	}
	if !hasUserMessage(p.histories[2], "next turn") {
		t.Error("the queued prompt was folded instead of running as its own turn")
	}
	if got := log.countKind(EventTurnEnd); got != 2 {
		t.Errorf("terminal events = %d, want one per turn (2)", got)
	}
	notices := 0
	for _, ev := range log.snapshot() {
		if ev.Kind == EventNotice && ev.Text == SteeringNotice {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("steering notices = %d, want 1", notices)
	}
	if !hasUserMessage(sess.History(), "next turn") {
		t.Error("queued turn's prompt missing from the transcript")
	}
}

// A call enqueued AFTER a Cancel (its accept predates the mark) is settled by
// the mark test at entry — the queue-clear alone cannot catch it.
func TestEnqueueAfterCancelSettlesAtEntry(t *testing.T) {
	p := newDispatchProvider()
	engine := engineWith(p, NewSession("m"))
	log := &dispatchLog{}
	engine.Observe = log.observe

	seq := engine.BeginAccepted()
	engine.CancelTurns()
	engine.EnqueueTurn(seq, "covered")

	text, qseq, ok := engine.NextQueuedTurn()
	if !ok || text != "covered" || qseq != seq {
		t.Fatalf("NextQueuedTurn = (%q, %d, %v), want (covered, %d, true)", text, qseq, ok, seq)
	}
	ran, err := engine.RunTurnAccepted(context.Background(), text, qseq)
	if ran || err != nil {
		t.Fatalf("covered entry = (%v, %v), want (false, nil)", ran, err)
	}
	end := oneTurnEnd(t, log.snapshot())
	if !end.Cancelled {
		t.Errorf("end = %+v, want a cancelled settlement", end)
	}
	if _, rounds := p.snapshot(); rounds != 0 {
		t.Errorf("provider ran %d rounds for a covered call", rounds)
	}
}

// Empty input is not a turn on any entry path: no accept, no events.
func TestDispatchEmptyInputIsNotATurn(t *testing.T) {
	engine := engineWith(newDispatchProvider(), NewSession("m"))
	log := &dispatchLog{}
	engine.Observe = log.observe

	if ran, err := engine.RunTurnAccepted(context.Background(), "   ", 0); ran || err != nil {
		t.Fatalf("empty accepted entry = (%v, %v), want (false, nil)", ran, err)
	}
	if err := engine.RunTurn(context.Background(), ""); err != nil {
		t.Fatalf("empty RunTurn = %v, want nil", err)
	}
	if len(log.snapshot()) != 0 {
		t.Errorf("empty input produced events: %v", eventKindSequence(log.snapshot()))
	}
	if engine.AcceptSequence() != 0 || engine.QueuedTurnCount() != 0 {
		t.Errorf("empty input advanced state: accept=%d queue=%d", engine.AcceptSequence(), engine.QueuedTurnCount())
	}
}

// FlushAll contract on settlement: the cancelled row lands in the store
// synchronously, before the terminal event — no debounce wait, no lost row.
func TestCancelOnEntryPersistsCancelledRow(t *testing.T) {
	st, rec, sess := newM9Store(t)
	engine := engineWith(newDispatchProvider(), sess)

	seq := engine.BeginAccepted()
	engine.CancelTurns()
	if ran, _ := engine.RunTurnAccepted(context.Background(), "persisted cancel", seq); ran {
		t.Fatal("covered call ran")
	}
	loaded, err := st.GetSession(rec.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("persisted %d messages, want the user row + cancelled row (2)", len(loaded.Messages))
	}
	if loaded.Messages[0].Role != "user" || loaded.Messages[0].Content != "persisted cancel" {
		t.Errorf("first persisted row = %+v, want the prompt", loaded.Messages[0])
	}
	if loaded.Messages[1].Role != "assistant" || loaded.Messages[1].Content != cancelledTurnNotice {
		t.Errorf("second persisted row = %+v, want the cancelled turn row", loaded.Messages[1])
	}
}
