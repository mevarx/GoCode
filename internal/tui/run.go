package tui

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/hooks"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/pubsub"
	"github.com/mevarx/GoCode/internal/tools"
)

// ThemeSettings is the [tui] theme configuration handed to the UI.
type ThemeSettings struct {
	// Name selects a built-in or user theme; empty means the default.
	Name string
	// Transparent skips painting the app background.
	Transparent bool
}

// SidebarInfo feeds the sidebar's session/MCP sections. The plan section is
// read live from the PlanStore instead, so it never goes stale mid-turn.
type SidebarInfo struct {
	SessionID    string
	SessionTitle string
	MCPServers   []string
}

// Run starts the Bubble Tea interface; agent behaviour lives in agent.AgentLoop.
func Run(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	version string,
	workspaceRoot string,
	guardCfg agent.LoopGuardConfig,
	maxContextTokens int,
	autoCompact bool,
	keepRecentTokens int,
	plan *tools.PlanStore,
	scratchpad *tools.ScratchpadStore,
	verifyCommand string,
	verifyTimeout time.Duration,
	motion MotionSettings,
	theme ThemeSettings,
	sidebar SidebarInfo,
	hookRunner *hooks.Runner,
) error {
	// Resolve the theme before any model exists: the style bag is global, so
	// doing it here means the very first frame is already painted correctly.
	applied := ApplyThemeSettings(theme.Name)

	bridge := NewApprovalBridge()
	// the danger-aware prompt hook carries the classifier reason into the
	// modal (Danger focus/warning). The bridge stays the transport; permission
	// payloads ride the bus in parallel for observers.
	approval.OnPresentPrompt = bridge.RequestApprovalPrompt

	engine := agent.NewAgentLoop(registry, session, toolRegistry, approval)
	engine.WorkspaceRoot = workspaceRoot
	engine.GuardConfig = guardCfg
	engine.ContextManager = agent.NewContextManager(maxContextTokens)
	engine.ContextManager.AutoCompact = autoCompact
	engine.ContextManager.KeepRecentTokens = keepRecentTokens
	engine.Plan = plan
	engine.Scratchpad = scratchpad
	engine.VerifyCommand = verifyCommand
	engine.VerifyTimeout = verifyTimeout
	// the hook engine is built once in main and shared by every UI path.
	// Nil means no hooks configured: zero overhead.
	engine.Hooks = hookRunner
	engine.EnsureSystemPrompt()

	// /commit reuses tool-approval modal so user sees the prompt.
	engine.AskApproval = func(prompt string) bool {
		approved, err := bridge.RequestApproval("/commit", json.RawMessage(`{}`), prompt)
		if err != nil {
			return false
		}
		return approved
	}

	inputCh := make(chan string, 1)
	// Steering is a separate channel so a message typed mid-turn can never be
	// mistaken for a new turn submission.
	steerCh := make(chan string, 8)
	outputCh := make(chan tea.Msg, 256)
	cancelCh := make(chan struct{}, 1)

	tuiCtx, tuiCancel := context.WithCancel(ctx)
	defer tuiCancel()

	// fan-in: the engine publishes LoopEvents on the bus; ONE pump
	// goroutine forwards them to the tea channel. Must-deliver end-to-end:
	// terminal events ride PublishMustDeliver into the pump (bounded 50ms
	// per subscriber, counted drops), and the pump forwards every event
	// with the ctx-aware blocking send below — it never silently drops a
	// terminal event. Backpressure past outputCh propagates into the bus
	// ring, where lossy drops are counted instead of stalling the engine.
	bus := pubsub.NewBroker[agent.LoopEvent]()
	engine.Bus = bus
	defer bus.Close()
	busEvents, busUnsub := bus.Subscribe()
	gate := &turnEndGate{}
	go pumpBusEvents(tuiCtx, busEvents, busUnsub, outputCh, gate)

	pickerItems := collectPickerItems(ctx, registry)
	if len(pickerItems) == 0 {
		pickerItems = append(pickerItems, ModelItem{
			Provider: registry.ActiveName(),
			Model:    session.Model(),
		})
	}

	m := NewModel(registry.ActiveName(), session.Model(), version, workspaceRoot, bridge, inputCh, steerCh, outputCh, cancelCh, pickerItems, motion)
	m.agentSession = session
	m.registry = registry
	// the queued-pills indicator reads the dispatcher's pending-turn
	// count live from the engine.
	m.engine = engine
	m.themeName = applied.Name
	m.transparent = theme.Transparent
	m.sidebar = sidebar
	m.planStore = plan
	// Per-workspace prompt history persists across restarts.
	m.enablePersistentHistory()

	go runEngineGoroutine(tuiCtx, engine, inputCh, steerCh, outputCh, cancelCh, gate)

	// AltScreen/mouse declared by View in v2, not program options.
	p := tea.NewProgram(m)

	_, err := p.Run()
	tuiCancel()
	return err
}

// Model collection bounds. Startup and the picker's refresh-on-open share
// them: one stalled provider must never defer the UI, and a refresh must never
// leave the list empty for longer than a glance.
const (
	pickerProviderTimeout = 2 * time.Second
	pickerOverallTimeout  = 3 * time.Second
)

// collectPickerItems queries every provider concurrently, bounded by a
// per-provider timeout and an overall deadline. It is the one collection path:
// startup builds the initial snapshot with it and the picker refreshes with it,
// so both can never disagree about what "a model list" is. A provider that
// fails or times out contributes nothing; the caller decides whether the result
// is worth installing.
func collectPickerItems(ctx context.Context, registry *provider.Registry) []list.Item {
	names := registry.List()
	if len(names) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, pickerOverallTimeout)
	defer cancel()
	// Buffered for every provider so a goroutine that finishes after the
	// deadline can always deliver and exit instead of leaking.
	ch := make(chan []list.Item, len(names))
	for _, name := range names {
		go func(name string) {
			p := registry.Get(name)
			if p == nil {
				ch <- nil
				return
			}
			mctx, mcancel := context.WithTimeout(ctx, pickerProviderTimeout)
			defer mcancel()
			models, err := p.Models(mctx)
			var items []list.Item
			if err == nil {
				for _, m := range models {
					items = append(items, ModelItem{Provider: name, Model: m})
				}
			}
			ch <- items
		}(name)
	}
	var items []list.Item
	for range names {
		select {
		case batch := <-ch:
			items = append(items, batch...)
		case <-ctx.Done():
			return sortedPickerItems(items)
		}
	}
	return sortedPickerItems(items)
}

func sortedPickerItems(items []list.Item) []list.Item {
	sort.Slice(items, func(i, j int) bool {
		a, aok := items[i].(ModelItem)
		b, bok := items[j].(ModelItem)
		if !aok || !bok {
			return false
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	return items
}

// Discards stale cancels so they can't stop next message (HIGH-9).
func drainCancelCh(cancelCh <-chan struct{}) {
	for {
		select {
		case <-cancelCh:
		default:
			return
		}
	}
}

func toTeaMsg(ev agent.LoopEvent) tea.Msg {
	switch ev.Kind {
	case agent.EventTurnStart:
		return agentTurnStartMsg{}
	case agent.EventTurnEnd:
		return agentTurnMetricsMsg{metrics: ev.Metrics, cancelled: ev.Cancelled}
	case agent.EventDelta:
		return agentChunkMsg{delta: ev.Text}
	case agent.EventToolStart:
		return agentToolStartMsg{name: ev.Tool, args: ev.Args, toolCallID: ev.ToolCallID}
	case agent.EventToolDelta:
		return agentToolDeltaMsg{name: ev.Tool, args: ev.Args, toolCallID: ev.ToolCallID}
	case agent.EventToolResult:
		return agentToolMsg{name: ev.Tool, result: ev.Text, isError: ev.IsError, toolCallID: ev.ToolCallID, diff: ev.Diff}
	case agent.EventError:
		return agentToolMsg{name: "GoCode", result: ev.Text, isError: true}
	case agent.EventNotice:
		return agentToolMsg{name: "GoCode", result: ev.Text, steeringApplied: ev.Text == agent.SteeringNotice}
	case agent.EventExit:
		return agentExitMsg{notice: ev.Text}
	default:
		return nil
	}
}

func sendMsg(ctx context.Context, outputCh chan<- tea.Msg, msg tea.Msg) {
	if msg == nil {
		return
	}
	select {
	case outputCh <- msg:
	case <-ctx.Done():
	}
}

// pumpBusEvents is the single fan-in goroutine: bus events → toTeaMsg →
// outputCh, in bus FIFO order. It exits on ctx cancellation or broker
// closure and unsubscribes on the way out. Every event is forwarded with the
// ctx-aware blocking send (sendMsg): a terminal event is never silently
// dropped here — if the UI stalls, the send waits, and upstream backpressure
// is absorbed (and counted) by the bus, not by losing the turn end.
func pumpBusEvents(ctx context.Context, events <-chan pubsub.Event[agent.LoopEvent], unsub func(), outputCh chan<- tea.Msg, gate *turnEndGate) {
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			sendMsg(ctx, outputCh, toTeaMsg(ev.Payload))
			// only a turn that actually ran signals the gate. Settlement
			// events (cancel-on-entry / dropped queue, Cancelled=true) must
			// not release the wait early — the running turn's own end event
			// is the one done must not overtake.
			if ev.Payload.Kind == agent.EventTurnEnd && !ev.Payload.Cancelled {
				gate.signal()
			}
		}
	}
}

// turnEndGate synchronizes the engine goroutine with the fan-in pump: the
// turn's agentDoneMsg must not overtake the bus events of that turn (the
// model flushes the stream buffer on done, so a late delta would be lost).
// The engine arms the gate before RunTurn; the pump signals it after
// forwarding EventTurnEnd.
type turnEndGate struct {
	mu sync.Mutex
	ch chan struct{}
}

func (g *turnEndGate) arm() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ch = make(chan struct{})
	return g.ch
}

func (g *turnEndGate) signal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch != nil {
		close(g.ch)
		g.ch = nil
	}
}

// disarm cancels an armed wait for a turn that never ran (cancel-on-entry
// or empty input): no primary end event will arrive, so the engine must not
// park on the gate until the fallback timeout.
func (g *turnEndGate) disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ch = nil
}

// turnEndGateTimeout bounds the engine's wait for the pump. The normal path
// completes in microseconds; the fallback only fires if the broker's
// must-deliver lane dropped the terminal event to a stalled subscriber
// (counted in MustDeliverDropCount) — 20× the 50ms bound, generous for CI.
const turnEndGateTimeout = time.Second

func waitForTurnEnd(ctx context.Context, ch <-chan struct{}) {
	if ch == nil {
		return
	}
	timer := time.NewTimer(turnEndGateTimeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-ctx.Done():
	case <-timer.C:
	}
}

// runEngineGoroutine is the serial turn executor.: acceptance is split
// from execution — acceptPromptLoop admits every inputCh prompt into the
// engine's dispatcher the moment it arrives (even mid-turn), and this
// goroutine drains the dispatcher FIFO, one full turn machinery per prompt.
// A prompt accepted while a turn runs is queued, never lost; a cancel landing
// between accept and RunTurn entry is honored by the dispatcher's mark test
// (cancel-on-entry). steerCh stays the separate mid-turn guidance lane.
func runEngineGoroutine(
	ctx context.Context,
	engine *agent.AgentLoop,
	inputCh <-chan string,
	steerCh <-chan string,
	outputCh chan<- tea.Msg,
	cancelCh <-chan struct{},
	gate *turnEndGate,
) {
	// work is a coalesced wakeup: the accept goroutine enqueues under the
	// dispatcher lock, then notifies. A buffered slot means a notification
	// landing mid-drain is never lost — the executor simply wakes again.
	work := make(chan struct{}, 1)
	acceptDone := make(chan struct{})
	go acceptPromptLoop(ctx, engine, inputCh, work, acceptDone)

	for {
		// Discard interrupts landing between turns so next turn doesn't consume them.
		drainCancelCh(cancelCh)

		// Drain everything queued — including work accepted before this
		// goroutine parked (or before it started) — one full turn machinery
		// per prompt, FIFO.
		for {
			text, seq, ok := engine.NextQueuedTurn()
			if !ok {
				break
			}
			// Re-drain per turn: an interrupt may have landed while the
			// previous turn ran or while this prompt sat queued (HIGH-9).
			drainCancelCh(cancelCh)
			// A steer typed after the previous turn ended belongs to no turn.
			drainSteerCh(steerCh)

			turnCtx, turnCancel := context.WithCancel(ctx)
			cancelWatcherDone := make(chan struct{})
			go watchCancelSignal(cancelCh, turnCtx, turnCancel, cancelWatcherDone, engine)
			steerDone := make(chan struct{})
			go pumpSteering(turnCtx, engine, steerCh, steerDone)

			// Arm the fan-in gate only for a turn that will actually run:
			// empty input emits no event, and a cancel-on-entry settlement
			// emits no primary end event (disarmed below).
			var turnEnd <-chan struct{}
			armed := gate != nil && strings.TrimSpace(text) != ""
			if armed {
				turnEnd = gate.arm()
			}
			ran, err := engine.RunTurnAccepted(turnCtx, text, seq)
			if armed && !ran {
				gate.disarm()
				turnEnd = nil
			}
			// Let the pump forward the turn's last bus event before done,
			// preserving the v0.7.5 record order (Observe was synchronous).
			waitForTurnEnd(ctx, turnEnd)
			if err != nil && !agent.IsExitRequest(err) {
				sendMsg(ctx, outputCh, agentToolMsg{name: "GoCode", result: err.Error(), isError: true})
			}
			sendMsg(ctx, outputCh, agentDoneMsg{err: err})

			turnCancel()
			<-steerDone
			<-cancelWatcherDone
		}

		select {
		case <-ctx.Done():
			<-acceptDone
			return
		case <-work:
		}
	}
}

// acceptPromptLoop admits inputCh prompts into the dispatcher as they arrive,
// mid-turn or idle, and wakes the executor. Acceptance is atomic
// (accept-sequence + enqueue in one critical section), so a Cancel can never
// slip between the two and lose a prompt.
func acceptPromptLoop(ctx context.Context, engine *agent.AgentLoop, inputCh <-chan string, work chan<- struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case input := <-inputCh:
			engine.AcceptTurn(input)
			select {
			case work <- struct{}{}:
			default:
			}
		}
	}
}

// pumpSteering hands mid-turn input to the engine while a turn runs. Steer
// rejects it once the turn is over, so a late send is harmless.
func pumpSteering(ctx context.Context, engine *agent.AgentLoop, steerCh <-chan string, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-steerCh:
			engine.Steer(text)
		}
	}
}

// drainSteerCh drops steering that outlived its turn, so it can't be applied
// to an unrelated later request.
func drainSteerCh(steerCh <-chan string) {
	for {
		select {
		case <-steerCh:
		default:
			return
		}
	}
}

// Must exit before next turn so it doesn't consume the next interrupt.
// an interrupt cancels through the dispatcher — it raises the cancel
// mark (covering prompts accepted but not yet entered), cancels the active
// turn's context and settles every queued prompt with a terminal event.
func watchCancelSignal(cancelCh <-chan struct{}, turnCtx context.Context, turnCancel context.CancelFunc, done chan<- struct{}, engine *agent.AgentLoop) {
	defer close(done)
	select {
	case <-cancelCh:
		engine.CancelTurns()
		turnCancel()
	case <-turnCtx.Done():
	}
}
