package tui

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

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
) error {
	bridge := NewApprovalBridge()
	approval.OnPresentChoice = bridge.RequestApprovalChoice

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

	engine.Observe = func(ev agent.LoopEvent) {
		sendMsg(tuiCtx, outputCh, toTeaMsg(ev))
	}

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

	go runEngineGoroutine(tuiCtx, engine, inputCh, steerCh, outputCh, cancelCh)

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
		return agentTurnMetricsMsg{metrics: ev.Metrics}
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

func runEngineGoroutine(
	ctx context.Context,
	engine *agent.AgentLoop,
	inputCh <-chan string,
	steerCh <-chan string,
	outputCh chan<- tea.Msg,
	cancelCh <-chan struct{},
) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Discard interrupts landing between turns so next turn doesn't consume them.
		drainCancelCh(cancelCh)

		select {
		case <-ctx.Done():
			return
		case input := <-inputCh:
			// Re-drain: interrupt may land while parked waiting for input (HIGH-9).
			drainCancelCh(cancelCh)
			// A steer typed after the previous turn ended belongs to no turn.
			drainSteerCh(steerCh)

			turnCtx, turnCancel := context.WithCancel(ctx)
			cancelWatcherDone := make(chan struct{})
			go watchCancelSignal(cancelCh, turnCtx, turnCancel, cancelWatcherDone)
			steerDone := make(chan struct{})
			go pumpSteering(turnCtx, engine, steerCh, steerDone)

			err := engine.RunTurn(turnCtx, input)
			if err != nil && !agent.IsExitRequest(err) {
				sendMsg(ctx, outputCh, agentToolMsg{name: "GoCode", result: err.Error(), isError: true})
			}
			sendMsg(ctx, outputCh, agentDoneMsg{err: err})

			turnCancel()
			<-steerDone
			<-cancelWatcherDone
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
func watchCancelSignal(cancelCh <-chan struct{}, turnCtx context.Context, turnCancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	select {
	case <-cancelCh:
		turnCancel()
	case <-turnCtx.Done():
	}
}
