package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/mevarx/GoCode/internal/provider"
)

// queued-turn dispatch state machine. One AgentLoop is
// one session, so the dispatcher below is the per-session authority for turn
// entry. Three states are decided atomically under one mutex:
//
// 1. cancel-on-entry — the call's accept-sequence is ≤ the session's cancel
// high-water mark: it never enters the stream. A cancelled turn row is
// persisted and a terminal cancelled event is emitted.
// 2. queued — a turn is already active: the call is appended to the FIFO
// queue and the entry returns immediately.
// 3. active — the call registers as the running turn (with its own cancel
// func) and executes.
//
// The accept-sequence machinery closes the window between "prompt accepted"
// and "turn entered": BeginAccepted increments a monotonic counter, Cancel
// raises the high-water mark to the current accept-sequence, and every entry
// (including each queued call when it is popped) is tested against the mark.
// A Cancel that lands in that window therefore settles the call instead of
// being lost. Cancellation is cooperative: the active stream returns
// context.Canceled and its partial assistant message is kept.

// cancelledTurnNotice is the assistant row persisted for a turn that was
// cancelled before it entered the stream. It keeps the transcript honest
// about why a user prompt has no model answer.
const cancelledTurnNotice = "[Turn cancelled before it started.]"

// queuedTurn is one accepted prompt waiting for its turn. seq is the accept
// handle it was admitted with; the mark test at entry uses it.
type queuedTurn struct {
	seq  uint64
	text string
}

// DispatchCall identifies one accepted prompt settled by CancelTurns, so a
// front-end can close its own per-prompt accounting (e.g. a headless
// turn_done record) for every dropped call.
type DispatchCall struct {
	Seq  uint64
	Text string
}

// dispatchState is the atomic outcome of a turn-entry decision.
type dispatchState int

const (
	dispatchActive dispatchState = iota
	dispatchQueued
	dispatchCancelOnEntry
)

// turnDispatcher holds the per-session dispatch state. Every field is guarded
// by mu; the three-state decision, the mark raise and the queue clear are
// each one critical section. Callbacks that emit events or run user code
// (settlement, the active cancel func) always run OUTSIDE the lock so a slow
// observer can never stall acceptance.
type turnDispatcher struct {
	mu sync.Mutex
	// acceptSeq is the monotonic accept counter (BeginAccepted).
	acceptSeq uint64
	// cancelMark is the cancel high-water mark: every call with seq ≤ mark
	// is settled on entry instead of running.
	cancelMark uint64
	// active registration. active stays true until the running turn calls
	// finish, even after Cancel cancelled its context: a prompt accepted
	// while the cancelled turn winds down must queue, not run concurrently.
	active       bool
	activeSeq    uint64
	activeCancel context.CancelFunc
	// queue is the FIFO of accepted prompts waiting for their own turn.
	queue []queuedTurn
}

// beginAccepted admits one call and returns its accept handle.
func (d *turnDispatcher) beginAccepted() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.acceptSeq++
	return d.acceptSeq
}

// accept admits and enqueues in ONE critical section, so no Cancel can slip
// between the sequence increment and the queue insertion. A fresh sequence
// always exceeds the mark (the mark never passes acceptSeq), so an accepted
// call is never born cancelled.
func (d *turnDispatcher) accept(text string) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.acceptSeq++
	seq := d.acceptSeq
	d.queue = append(d.queue, queuedTurn{seq: seq, text: text})
	return seq
}

// enqueue appends a previously accepted call (BeginAccepted happened earlier,
// possibly before a Cancel). The entry test against the mark settles it if
// the cancel window covered it.
func (d *turnDispatcher) enqueue(seq uint64, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queue = append(d.queue, queuedTurn{seq: seq, text: text})
}

// enter decides the three states atomically. On the active path it derives
// the run context and registers its cancel func under the same lock, so a
// Cancel racing entry either wins the mark test (cancel-on-entry) or finds
// the registration and cancels it — there is no gap where an active turn
// has no cancel handle.
func (d *turnDispatcher) enter(ctx context.Context, seq uint64, text string) (dispatchState, context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if seq <= d.cancelMark {
		return dispatchCancelOnEntry, ctx
	}
	if d.active {
		d.queue = append(d.queue, queuedTurn{seq: seq, text: text})
		return dispatchQueued, ctx
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.active = true
	d.activeSeq = seq
	d.activeCancel = cancel
	return dispatchActive, runCtx
}

// finish deregisters the active turn, compare-and-delete style: a finishing
// older run never wipes a newer run's registration. The run context's cancel
// func is invoked outside the lock to release its resources.
func (d *turnDispatcher) finish(seq uint64) {
	d.mu.Lock()
	var cancel context.CancelFunc
	if d.active && d.activeSeq == seq {
		d.active = false
		d.activeSeq = 0
		cancel = d.activeCancel
		d.activeCancel = nil
	}
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// next pops the oldest queued call (FIFO).
func (d *turnDispatcher) next() (queuedTurn, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return queuedTurn{}, false
	}
	call := d.queue[0]
	d.queue = d.queue[1:]
	return call, true
}

// cancel raises the high-water mark to the current accept-sequence, takes
// the active cancel func and takes the whole queue. The active registration
// itself stays (the cancelled turn is still winding down); the caller
// invokes the cancel func and settles each dropped call outside the lock.
func (d *turnDispatcher) cancel() (context.CancelFunc, []queuedTurn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.acceptSeq > d.cancelMark {
		d.cancelMark = d.acceptSeq
	}
	fn := d.activeCancel
	d.activeCancel = nil
	dropped := d.queue
	d.queue = nil
	return fn, dropped
}

func (d *turnDispatcher) queuedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queue)
}

func (d *turnDispatcher) marks() (accept, mark uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.acceptSeq, d.cancelMark
}

// BeginAccepted admits a prompt and returns its monotonic accept sequence.
// Front-ends call it at accept time (when the prompt arrives), then enter the
// turn later with RunTurnAccepted; a Cancel landing in between is honored by
// the mark test at entry (cancel-on-entry).
func (a *AgentLoop) BeginAccepted() uint64 {
	return a.dispatch.beginAccepted()
}

// AcceptTurn admits and enqueues a prompt atomically (fire-and-forget
// acceptance). The prompt runs as its own turn when the executor drains the
// queue; it is never folded into a running stream — mid-turn guidance is
// Steer's job. Returns the accept sequence.
func (a *AgentLoop) AcceptTurn(text string) uint64 {
	return a.dispatch.accept(text)
}

// EnqueueTurn queues a prompt accepted earlier with BeginAccepted. If a
// Cancel covered its sequence in the meantime, the entry test in
// RunTurnAccepted settles it as cancel-on-entry.
func (a *AgentLoop) EnqueueTurn(seq uint64, text string) {
	a.dispatch.enqueue(seq, text)
}

// NextQueuedTurn pops the oldest queued prompt (FIFO). The executor enters it
// with RunTurnAccepted, which re-tests the cancel mark atomically.
func (a *AgentLoop) NextQueuedTurn() (text string, seq uint64, ok bool) {
	call, ok := a.dispatch.next()
	return call.text, call.seq, ok
}

// QueuedTurnCount reports how many accepted prompts are waiting for their
// own turn. The TUI queued-pills indicator reads it live.
func (a *AgentLoop) QueuedTurnCount() int {
	return a.dispatch.queuedCount()
}

// AcceptSequence reports the current monotonic accept counter.
func (a *AgentLoop) AcceptSequence() uint64 {
	accept, _ := a.dispatch.marks()
	return accept
}

// CancelMark reports the current cancel high-water mark.
func (a *AgentLoop) CancelMark() uint64 {
	_, mark := a.dispatch.marks()
	return mark
}

// CancelTurns cancels the session's dispatch: it raises the cancel mark to
// the current accept-sequence, cancels the active turn's context
// (cooperatively — the stream returns context.Canceled and its partial
// assistant message is kept), and clears the queue, settling EVERY dropped
// prompt with a persisted cancelled turn row and a terminal cancelled event
// so no caller hangs. It returns the settled calls in queue order so a
// front-end can close its per-prompt accounting. Prompts accepted after this
// call carry sequences above the mark and run normally.
func (a *AgentLoop) CancelTurns() []DispatchCall {
	fn, dropped := a.dispatch.cancel()
	if fn != nil {
		fn()
	}
	settled := make([]DispatchCall, 0, len(dropped))
	for _, call := range dropped {
		a.settleCancelledTurn(call.text)
		settled = append(settled, DispatchCall{Seq: call.seq, Text: call.text})
	}
	return settled
}

// settleCancelledTurn persists the cancelled turn row and emits its terminal
// event. The turn never entered the stream, so there is no TurnStart and no
// metrics — exactly one EventTurnEnd, flagged Cancelled. Empty prompts are
// not turns (RunTurn ignores them without events) and settle silently.
func (a *AgentLoop) settleCancelledTurn(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if a.Session != nil {
		a.Session.AddMessage(provider.Message{Role: "user", Content: text})
		a.Session.AddMessage(provider.Message{Role: "assistant", Content: cancelledTurnNotice})
		// FlushAll contract: the cancelled row lands before the terminal
		// event, so a cut-short queue loses nothing.
		a.Session.FlushMessages()
	}
	a.emit(LoopEvent{Kind: EventTurnEnd, Err: context.Canceled, Cancelled: true})
}

// RunTurn processes one user input synchronously: slash command or full agent
// turn with tools. It is the self-contained entry for serial callers (the
// plain REPL, tests): it accepts the input now, and if a concurrent call
// queues behind it, it drains the queue FIFO before returning — each queued
// prompt runs as its own turn with its own terminal event. A turn that runs
// reports EventTurnStart before anything else and exactly one EventTurnEnd,
// whatever its outcome.
func (a *AgentLoop) RunTurn(ctx context.Context, input string) error {
	_, err := a.runTurnDispatched(ctx, input, 0, true)
	return err
}

// RunTurnAccepted enters a previously accepted prompt (seq from
// BeginAccepted/AcceptTurn) through the dispatch state machine. It is the
// executor entry for front-ends that drive the queue themselves (TUI,
// headless): it does NOT drain the queue, so the front-end keeps one
// turn-end handshake and one done record per turn. ran reports whether the
// turn actually executed: false for cancel-on-entry (settled with a terminal
// cancelled event), queued (a concurrent turn is active) and empty input.
// A seq of 0 accepts the input now.
func (a *AgentLoop) RunTurnAccepted(ctx context.Context, input string, seq uint64) (ran bool, err error) {
	return a.runTurnDispatched(ctx, input, seq, false)
}

// runTurnDispatched is the single dispatch authority. seq 0 means "accept
// now". The three-state decision is atomic; execution itself runs outside
// the dispatcher lock. When drain is set, the queue is emptied FIFO after
// the turn finishes, each call re-entering through the same mark test.
func (a *AgentLoop) runTurnDispatched(ctx context.Context, input string, seq uint64, drain bool) (ran bool, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return false, nil
	}
	if seq == 0 {
		seq = a.dispatch.beginAccepted()
	}
	state, runCtx := a.dispatch.enter(ctx, seq, input)
	switch state {
	case dispatchCancelOnEntry:
		a.settleCancelledTurn(input)
		return false, nil
	case dispatchQueued:
		return false, nil
	}

	err = a.executeTurn(runCtx, input)
	a.dispatch.finish(seq)

	if drain {
		for {
			call, ok := a.dispatch.next()
			if !ok {
				break
			}
			// Each queued call is re-tested against the mark at entry and
			// runs as its own turn (own steering window, hooks, metrics and
			// exactly-one terminal event). The first error wins the return
			// value; every call still settles, so none is left hanging.
			if _, qerr := a.runTurnDispatched(ctx, call.text, call.seq, true); qerr != nil && err == nil {
				err = qerr
			}
		}
	}
	return true, err
}
