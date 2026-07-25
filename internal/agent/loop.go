package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gocode-cli/gocode/internal/provider"
	"github.com/gocode-cli/gocode/internal/tools"
)

// AgentLoop coordinates user prompts, model interactions, and tool executions.
type AgentLoop struct {
	Registry     *provider.Registry
	Session      *Session
	ToolRegistry *tools.Registry
	Approval     *tools.ApprovalGate
}

// NewAgentLoop creates a new AgentLoop instance.
func NewAgentLoop(registry *provider.Registry, session *Session, toolReg *tools.Registry, approval *tools.ApprovalGate) *AgentLoop {
	return &AgentLoop{
		Registry:     registry,
		Session:      session,
		ToolRegistry: toolReg,
		Approval:     approval,
	}
}

func (a *AgentLoop) toolSpecsAsProvider() []provider.ToolSpec {
	toolSpecs := a.ToolRegistry.Specs()
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

// Run executes the interactive loop until exit command or context cancellation.
func (a *AgentLoop) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(os.Stdin)

	a.Session.AddMessage(provider.Message{
		Role: "system",
		Content: `You are GoCode, a helpful coding assistant running in the terminal. You help users with programming tasks.
You have access to tools for executing shell commands and reading/writing files.
Be concise and direct. When you need to perform actions, use the available tools.`,
	})

	fmt.Println("GoCode — Terminal Coding Agent")
	fmt.Printf("Provider: %s | Model: %s\n", a.Registry.ActiveName(), a.Session.Model())
	if toolNames := a.ToolRegistry.List(); len(toolNames) > 0 {
		fmt.Printf("Tools: %s\n", strings.Join(toolNames, ", "))
	}
	fmt.Println("Type your message (or 'exit' to quit)")
	fmt.Println(strings.Repeat("─", 50))

	for {
		fmt.Print("\n> ")

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())

		if input == "" {
			continue
		}

		lowerInput := strings.ToLower(input)
		switch {
		case lowerInput == "exit" || lowerInput == "quit":
			fmt.Println("Goodbye!")
			return nil
		case lowerInput == "/clear":
			a.Session.Clear()
			fmt.Println("[Session cleared]")
			continue
		case lowerInput == "/providers" || lowerInput == "/provider":
			active := a.Registry.ActiveName()
			available := a.Registry.List()
			fmt.Printf("Active Provider: %s\nAvailable Providers: %s\n", active, strings.Join(available, ", "))
			if models, err := a.Registry.Active().Models(ctx); err == nil && len(models) > 0 {
				fmt.Printf("Available Models: %s\n", strings.Join(models, ", "))
			}
			continue
		case strings.HasPrefix(lowerInput, "/provider "):
			target := strings.TrimSpace(input[10:])
			if err := a.Registry.Switch(target); err != nil {
				fmt.Printf("Error switching provider: %v\n", err)
			} else {
				fmt.Printf("[Provider switched to %s]\n", target)
				// If provider has default model or models list, select first available if active model empty or incompatible
				if models, err := a.Registry.Active().Models(ctx); err == nil && len(models) > 0 {
					fmt.Printf("Available models for %s: %s\n", target, strings.Join(models, ", "))
				}
			}
			continue
		case lowerInput == "/model":
			fmt.Printf("Active Model: %s\n", a.Session.Model())
			if models, err := a.Registry.Active().Models(ctx); err == nil && len(models) > 0 {
				fmt.Printf("Available Models (%s): %s\n", a.Registry.ActiveName(), strings.Join(models, ", "))
			} else if err != nil {
				fmt.Printf("Could not query models from %s: %v\n", a.Registry.ActiveName(), err)
			}
			continue
		case strings.HasPrefix(lowerInput, "/model "):
			targetModel := strings.TrimSpace(input[7:])
			a.Session.SetModel(targetModel)
			fmt.Printf("[Model set to %s]\n", targetModel)
			continue
		}

		a.Session.AddMessage(provider.Message{
			Role:    "user",
			Content: input,
		})

		if err := a.streamResponse(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stdin: %w", err)
	}

	return nil
}

func (a *AgentLoop) streamResponse(ctx context.Context) error {
	p := a.Registry.Active()
	model := a.Session.Model()
	providerToolSpecs := a.toolSpecsAsProvider()

	ch, err := p.Stream(ctx, model, a.Session.History(), providerToolSpecs)
	if err != nil {
		return fmt.Errorf("stream error: %w", err)
	}

	var fullResponse strings.Builder
	var toolCalls []provider.ToolCall

	fmt.Print("\n")
	for chunk := range ch {
		if chunk.Err != nil {
			return fmt.Errorf("stream chunk error: %w", chunk.Err)
		}

		if chunk.Delta != "" {
			fmt.Print(chunk.Delta)
			fullResponse.WriteString(chunk.Delta)
		}

		if len(chunk.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.ToolCalls...)
		}
	}

	if fullResponse.Len() > 0 {
		fmt.Println()
	}

	assistantMsg := provider.Message{
		Role:      "assistant",
		Content:   fullResponse.String(),
		ToolCalls: toolCalls,
	}
	a.Session.AddMessage(assistantMsg)

	if len(toolCalls) > 0 {
		return a.handleToolCalls(ctx, toolCalls)
	}

	return nil
}

func (a *AgentLoop) handleToolCalls(ctx context.Context, toolCalls []provider.ToolCall) error {
	for _, tc := range toolCalls {
		tool := a.ToolRegistry.Get(tc.Name)
		if tool == nil {
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Error: unknown tool %q", tc.Name),
				ToolCallID: tc.ID,
			})
			continue
		}

		result, err := a.Approval.WrapExecution(ctx, tool, json.RawMessage(tc.Args))
		if err != nil {
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Error executing %s: %v", tc.Name, err),
				ToolCallID: tc.ID,
			})
			continue
		}

		content := result.String()
		if result.Diff != "" {
			content = fmt.Sprintf("%s\n\nDiff:\n%s", content, result.Diff)
		}

		a.Session.AddMessage(provider.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: tc.ID,
		})

		fmt.Printf("\n[%s result]\n", tc.Name)
		if result.Error != "" {
			fmt.Printf("Error: %s\n", result.Error)
		} else if result.Output != "" {
			output := result.Output
			if len(output) > 2000 {
				output = output[:2000] + "\n... (truncated)"
			}
			fmt.Println(output)
		}
	}

	return a.streamResponse(ctx)
}
