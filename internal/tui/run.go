package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// Run starts the Bubble Tea interface over the shared agent engine.
//
// All agent behaviour — streaming, tool execution, approval, context
// truncation and the loop guard — lives in agent.AgentLoop. This file only
// translates engine events into tea messages and routes user input back in.
func Run(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	version string,
	workspaceRoot string,
	guardCfg agent.LoopGuardConfig,
) error {
	bridge := NewApprovalBridge()
	approval.OnPresent = bridge.RequestApproval

	// The engine: one instance, shared by every turn in this session.
	engine := agent.NewAgentLoop(registry, session, toolRegistry, approval)
	engine.WorkspaceRoot = workspaceRoot
	engine.GuardConfig = guardCfg
	engine.EnsureSystemPrompt()

	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 256)
	cancelCh := make(chan struct{}, 1)

	tuiCtx, tuiCancel := context.WithCancel(ctx)
	defer tuiCancel()

	// Bridge engine events onto the tea message channel.
	engine.Observe = func(ev agent.LoopEvent) {
		sendMsg(tuiCtx, outputCh, toTeaMsg(ev))
	}

	var pickerItems []list.Item
	for _, pName := range registry.List() {
		p := registry.Get(pName)
		if p != nil {
			if models, err := p.Models(ctx); err == nil && len(models) > 0 {
				for _, m := range models {
					pickerItems = append(pickerItems, ModelItem{
						Provider: pName,
						Model:    m,
					})
				}
			}
		}
	}
	if len(pickerItems) == 0 {
		pickerItems = append(pickerItems, ModelItem{
			Provider: registry.ActiveName(),
			Model:    session.Model(),
		})
	}

	m := NewModel(registry.ActiveName(), session.Model(), version, workspaceRoot, bridge, inputCh, outputCh, cancelCh, pickerItems)

	go runEngineGoroutine(tuiCtx, engine, inputCh, outputCh, cancelCh)

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	tuiCancel()
	return err
}

// toTeaMsg converts an engine event into the message the TUI renders.
func toTeaMsg(ev agent.LoopEvent) tea.Msg {
	switch ev.Kind {
	case agent.EventDelta:
		return agentChunkMsg{delta: ev.Text}
	case agent.EventToolResult:
		return agentToolMsg{name: ev.Tool, result: ev.Text, isError: ev.IsError}
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

// runEngineGoroutine feeds submitted input into the engine one turn at a time,
// wiring Ctrl+C / Esc cancellation to the active turn.
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
		case input := <-inputCh:
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

// watchCancelSignal cancels the active turn when the user interrupts, until
// the turn itself ends. It must exit before the next turn consumes the next
// interrupt signal, hence the done channel join.
func watchCancelSignal(cancelCh <-chan struct{}, turnCtx context.Context, turnCancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	select {
	case <-cancelCh:
		turnCancel()
	case <-turnCtx.Done():
	}
}
