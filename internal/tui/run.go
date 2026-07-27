package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
) error {
	bridge := NewApprovalBridge()
	approval.OnPresent = bridge.RequestApproval

	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 32)

	m := NewModel(registry.ActiveName(), session.Model(), bridge, inputCh, outputCh)

	go runAgentGoroutine(ctx, registry, session, toolRegistry, approval, inputCh, outputCh)

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	return err
}

func runAgentGoroutine(
	ctx context.Context,
	registry *provider.Registry,
	session *agent.Session,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	inputCh <-chan string,
	outputCh chan<- tea.Msg,
) {
	toolSpecs := toolSpecsAsProvider(toolRegistry)

	for {
		select {
		case <-ctx.Done():
			return
		case input := <-inputCh:
			if handled := handleSlashCommand(ctx, input, registry, session, outputCh); handled {
				outputCh <- agentDoneMsg{}
				continue
			}

			session.AddMessage(provider.Message{Role: "user", Content: input})

			if err := runTurn(ctx, registry, session, toolRegistry, approval, toolSpecs, outputCh); err != nil {
				outputCh <- agentDoneMsg{err: err}
			} else {
				outputCh <- agentDoneMsg{}
			}
		}
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
	ch, err := registry.Active().Stream(ctx, session.Model(), session.History(), toolSpecs)
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
			outputCh <- agentChunkMsg{delta: chunk.Delta}
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.ToolCalls...)
		}
	}

	session.AddMessage(provider.Message{
		Role:      "assistant",
		Content:   fullResponse.String(),
		ToolCalls: toolCalls,
	})

	if len(toolCalls) > 0 {
		return handleToolCalls(ctx, toolCalls, toolRegistry, approval, session, registry, toolSpecs, outputCh)
	}

	return nil
}

func handleToolCalls(
	ctx context.Context,
	toolCalls []provider.ToolCall,
	toolRegistry *tools.Registry,
	approval *tools.ApprovalGate,
	session *agent.Session,
	registry *provider.Registry,
	toolSpecs []provider.ToolSpec,
	outputCh chan<- tea.Msg,
) error {
	for _, tc := range toolCalls {
		tool := toolRegistry.Get(tc.Name)
		if tool == nil {
			errMsg := fmt.Sprintf("unknown tool %q", tc.Name)
			session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    "Error: " + errMsg,
				ToolCallID: tc.ID,
			})
			outputCh <- agentToolMsg{name: tc.Name, result: errMsg, isError: true}
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
			outputCh <- agentToolMsg{name: tc.Name, result: errContent, isError: true}
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
		outputCh <- agentToolMsg{name: tc.Name, result: display, isError: result.Error != ""}
	}

	return runTurn(ctx, registry, session, toolRegistry, approval, toolSpecs, outputCh)
}

func handleSlashCommand(
	ctx context.Context,
	input string,
	registry *provider.Registry,
	session *agent.Session,
	outputCh chan<- tea.Msg,
) bool {
	lower := strings.ToLower(strings.TrimSpace(input))

	switch {
	case lower == "/clear":
		session.Clear()
		outputCh <- agentToolMsg{name: "Session", result: "cleared"}
		return true

	case lower == "/providers" || lower == "/provider":
		info := fmt.Sprintf("Active: %s | Available: %s", registry.ActiveName(), strings.Join(registry.List(), ", "))
		if models, err := registry.Active().Models(ctx); err == nil && len(models) > 0 {
			info += "\nModels: " + strings.Join(models, ", ")
		}
		outputCh <- agentToolMsg{name: "Providers", result: info}
		return true

	case strings.HasPrefix(lower, "/provider "):
		target := strings.TrimSpace(input[10:])
		if err := registry.Switch(target); err != nil {
			outputCh <- agentToolMsg{name: "Provider", result: fmt.Sprintf("error: %v", err), isError: true}
		} else {
			outputCh <- agentToolMsg{name: "Provider", result: fmt.Sprintf("switched to %s", target)}
		}
		return true

	case lower == "/model":
		info := "Active model: " + session.Model()
		if models, err := registry.Active().Models(ctx); err == nil && len(models) > 0 {
			info += "\nAvailable: " + strings.Join(models, ", ")
		}
		outputCh <- agentToolMsg{name: "Model", result: info}
		return true

	case strings.HasPrefix(lower, "/model "):
		target := strings.TrimSpace(input[7:])
		session.SetModel(target)
		outputCh <- agentToolMsg{name: "Model", result: fmt.Sprintf("set to %s", target)}
		return true
	}

	return false
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
