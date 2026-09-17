package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

func Run(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	version string,
	workspaceRoot string,
) error {
	bridge := NewApprovalBridge()
	approval.OnPresent = bridge.RequestApproval

	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 256)
	cancelCh := make(chan struct{}, 1)

	tuiCtx, tuiCancel := context.WithCancel(ctx)
	defer tuiCancel()

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

	m := NewModel(registry.ActiveName(), session.Model(), version, bridge, inputCh, outputCh, cancelCh, pickerItems)

	go runAgentGoroutine(tuiCtx, registry, session, toolRegistry, approval, inputCh, outputCh, cancelCh, workspaceRoot)

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	tuiCancel()
	return err
}

func sendMsg(ctx context.Context, outputCh chan<- tea.Msg, msg tea.Msg) {
	select {
	case outputCh <- msg:
	case <-ctx.Done():
	}
}

func runAgentGoroutine(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	inputCh <-chan string,
	outputCh chan<- tea.Msg,
	cancelCh <-chan struct{},
	workspaceRoot string,
) {
	toolSpecs := toolSpecsAsProvider(toolRegistry)

	for {
		select {
		case <-ctx.Done():
			return
		case input := <-inputCh:
			turnCtx, turnCancel := context.WithCancel(ctx)
			cancelWatcherDone := make(chan struct{})
			go watchCancelSignal(cancelCh, turnCtx, turnCancel, cancelWatcherDone)

			if handled := handleSlashCommand(turnCtx, input, registry, session, outputCh, workspaceRoot); handled {
				sendMsg(ctx, outputCh, agentDoneMsg{})
				turnCancel()
				<-cancelWatcherDone
				continue
			}

			session.AddMessage(provider.Message{Role: "user", Content: input})

			err := runTurn(turnCtx, registry, session, toolRegistry, approval, toolSpecs, outputCh)
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

func runTurn(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	toolSpecs []provider.ToolSpec,
	outputCh chan<- tea.Msg,
) error {
	for {
		p, err := registry.Active()
		if err != nil {
			return fmt.Errorf("active provider error: %w", err)
		}
		ch, err := p.Stream(ctx, session.Model(), session.History(), toolSpecs)
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		var fullResponse strings.Builder
		var toolCalls []provider.ToolCall

		for chunk := range ch {
			if chunk.Err != nil {
				return fmt.Errorf("stream chunk error: %w", chunk.Err)
			}
			if chunk.Delta != "" {
				fullResponse.WriteString(chunk.Delta)
				sendMsg(ctx, outputCh, agentChunkMsg{delta: chunk.Delta})
			}
			if len(chunk.ToolCalls) > 0 {
				toolCalls = append(toolCalls, chunk.ToolCalls...)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		session.AddMessage(provider.Message{
			Role:      "assistant",
			Content:   fullResponse.String(),
			ToolCalls: toolCalls,
		})

		if len(toolCalls) == 0 {
			return nil
		}

		if err := handleToolCalls(ctx, toolCalls, toolRegistry, approval, session, outputCh); err != nil {
			return err
		}
	}
}

func handleToolCalls(
	ctx context.Context,
	toolCalls []provider.ToolCall,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	session *agent.Session,
	outputCh chan<- tea.Msg,
) error {
	for _, tc := range toolCalls {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		tool := toolRegistry.Get(tc.Name)
		if tool == nil {
			errMsg := fmt.Sprintf("unknown tool %q", tc.Name)
			session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    "Error: " + errMsg,
				ToolCallID: tc.ID,
			})
			sendMsg(ctx, outputCh, agentToolMsg{name: tc.Name, result: errMsg, isError: true})
			continue
		}

		result, err := approval.WrapExecution(ctx, tool, json.RawMessage(tc.Args))
		if err != nil {
			errContent := fmt.Sprintf("execution error: %v", err)
			session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    errContent,
				ToolCallID: tc.ID,
			})
			sendMsg(ctx, outputCh, agentToolMsg{name: tc.Name, result: errContent, isError: true})
			continue
		}

		content := result.String()
		if result.Diff != "" {
			content = content + "\n\nDiff:\n" + result.Diff
		}
		session.AddMessage(provider.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: tc.ID,
		})

		display := result.Output
		if result.Error != "" {
			display = result.Error
		}
		if len(display) > 2000 {
			display = display[:2000] + "\n… (truncated)"
		}
		sendMsg(ctx, outputCh, agentToolMsg{name: tc.Name, result: display, isError: result.Error != ""})
	}

	return nil
}

func handleSlashCommand(
	ctx context.Context,
	input string,
	registry *provider.Registry,
	session *agent.Session,
	outputCh chan<- tea.Msg,
	workspaceRoot string,
) bool {
	cmdCtx := agent.CommandContext{
		Session:        session,
		Registry:       registry,
		ContextManager: agent.NewContextManager(0),
		WorkspaceRoot:  workspaceRoot,
	}

	res := agent.HandleCommand(ctx, cmdCtx, input)
	if !res.Handled {
		return false
	}

	if res.Error != nil {
		sendMsg(ctx, outputCh, agentToolMsg{name: "Command", result: fmt.Sprintf("Error: %v", res.Error), isError: true})
	} else if res.Output != "" {
		output := res.Output
		if strings.TrimSpace(input) == "/help" {
			output += "\n  Ctrl+L           — open interactive model picker\n" +
				"  Ctrl+C           — stop a running turn, or quit when idle\n" +
				"  Esc              — stop a running turn, or clear current input\n" +
				"  exit             — quit GoCode"
		}
		sendMsg(ctx, outputCh, agentToolMsg{name: "Command", result: output})
	}
	return true
}

func toolSpecsAsProvider(toolRegistry *tools.Registry) []provider.ToolSpec {
	toolSpecs := toolRegistry.Specs()
	providerSpecs := make([]provider.ToolSpec, len(toolSpecs))
	for i, ts := range toolSpecs {
		providerSpecs[i] = provider.ToolSpec{
			Name:        ts.Name,
			Description: ts.Description,
			Parameters:  ts.Parameters,
		}
	}
	return providerSpecs
}
