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
) error {
	bridge := NewApprovalBridge()
	approval.OnPresent = bridge.RequestApproval

	engine := agent.NewAgentLoop(registry, session, toolRegistry, approval)
	engine.WorkspaceRoot = workspaceRoot
	engine.GuardConfig = guardCfg
	engine.ContextManager = agent.NewContextManager(maxContextTokens)
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

	m := NewModel(registry.ActiveName(), session.Model(), version, workspaceRoot, bridge, inputCh, outputCh, cancelCh, pickerItems)
	m.agentSession = session
	m.registry = registry

	go runEngineGoroutine(tuiCtx, engine, inputCh, outputCh, cancelCh)

	// AltScreen/mouse declared by View in v2, not program options.
	p := tea.NewProgram(m)

	_, err := p.Run()
	tuiCancel()
	return err
}

// Concurrent with deadline; sequential fetch let one stalled server defer startup.
func collectPickerItems(ctx context.Context, registry *provider.Registry) []list.Item {
	names := registry.List()
	if len(names) == 0 {
		return nil
	}
	ch := make(chan []list.Item, len(names))
	for _, name := range names {
		go func(name string) {
			p := registry.Get(name)
			if p == nil {
				ch <- nil
				return
			}
			mctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
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
	deadline := time.After(3 * time.Second)
	var items []list.Item
	for range names {
		select {
		case batch := <-ch:
			items = append(items, batch...)
		case <-deadline:
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
	case agent.EventDelta:
		return agentChunkMsg{delta: ev.Text}
	case agent.EventToolStart:
		return agentToolStartMsg{name: ev.Tool, args: ev.Args, toolCallID: ev.ToolCallID}
	case agent.EventToolResult:
		return agentToolMsg{name: ev.Tool, result: ev.Text, isError: ev.IsError, toolCallID: ev.ToolCallID, diff: ev.Diff}
	case agent.EventError:
		return agentToolMsg{name: "GoCode", result: ev.Text, isError: true}
	case agent.EventNotice:
		return agentToolMsg{name: "GoCode", result: ev.Text}
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

			turnCtx, turnCancel := context.WithCancel(ctx)
			cancelWatcherDone := make(chan struct{})
			go watchCancelSignal(cancelCh, turnCtx, turnCancel, cancelWatcherDone)

			err := engine.RunTurn(turnCtx, input)
			if err != nil && !agent.IsExitRequest(err) {
				sendMsg(ctx, outputCh, agentToolMsg{name: "GoCode", result: err.Error(), isError: true})
			}
			sendMsg(ctx, outputCh, agentDoneMsg{err: err})

			turnCancel()
			<-cancelWatcherDone
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
