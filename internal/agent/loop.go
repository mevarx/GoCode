package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

type AgentLoop struct {
	Registry       *provider.Registry
	Session        *Session
	ToolRegistry   *tools.Registry
	Approval       *tools.ApprovalGate
	ContextManager *ContextManager
	WorkspaceRoot  string
}

func NewAgentLoop(registry *provider.Registry, session *Session, toolReg *tools.Registry, approval *tools.ApprovalGate) *AgentLoop {
	return &AgentLoop{
		Registry:       registry,
		Session:        session,
		ToolRegistry:   toolReg,
		Approval:       approval,
		ContextManager: NewContextManager(0),
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

func (a *AgentLoop) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(os.Stdin)

	hasSystemMsg := false
	for _, m := range a.Session.History() {
		if m.Role == "system" {
			hasSystemMsg = true
			break
		}
	}

	if !hasSystemMsg {
		opts := DefaultPromptOptions(a.WorkspaceRoot, a.Registry.ActiveName(), a.Session.Model())
		sysPrompt := BuildSystemPromptWithOptions(opts)
		a.Session.AddMessage(provider.Message{
			Role:    "system",
			Content: sysPrompt,
		})
	}

	fmt.Println("GoCode — Terminal Coding Agent")
	fmt.Printf("Session: %s | Provider: %s | Model: %s\n", a.Session.ID(), a.Registry.ActiveName(), a.Session.Model())
	if toolNames := a.ToolRegistry.List(); len(toolNames) > 0 {
		fmt.Printf("Tools: %s\n", strings.Join(toolNames, ", "))
	}
	fmt.Println("Type your message (or 'exit' to quit, '/help' for commands)")
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

		cmdCtx := CommandContext{
			Session:        a.Session,
			Registry:       a.Registry,
			ContextManager: a.ContextManager,
			WorkspaceRoot:  a.WorkspaceRoot,
			AskApproval: func(prompt string) bool {
				fmt.Printf("%s [y/N]: ", prompt)
				if !scanner.Scan() {
					return false
				}
				ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
				return ans == "y" || ans == "yes"
			},
		}

		cmdRes := HandleCommand(ctx, cmdCtx, input)
		if cmdRes.Handled {
			if cmdRes.Exit {
				if cmdRes.Output != "" {
					fmt.Println(cmdRes.Output)
				}
				return nil
			}
			if cmdRes.Error != nil {
				fmt.Printf("Error: %v\n", cmdRes.Error)
			} else if cmdRes.Output != "" {
				fmt.Println(cmdRes.Output)
			}
			continue
		}

		a.Session.AddMessage(provider.Message{
			Role:    "user",
			Content: input,
		})

		if err := a.streamResponse(ctx); err != nil {
			slog.Error("stream failed", "error", err)
			fmt.Printf("\nError: %v\n", err)
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stdin: %w", err)
	}

	return nil
}

func (a *AgentLoop) streamResponse(ctx context.Context) error {
	for {
		p, err := a.Registry.Active()
		if err != nil {
			return err
		}
		model := a.Session.Model()
		providerToolSpecs := a.toolSpecsAsProvider()

		history := a.ContextManager.Truncate(a.Session.History())
		slog.Debug("streaming request", "provider", a.Registry.ActiveName(), "model", model, "history_len", len(history))

		ch, err := p.Stream(ctx, model, history, providerToolSpecs)
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
		} else if len(toolCalls) == 0 {
			fmt.Printf("[No response received from provider %q. Verify provider API keys/credentials or switch with /provider]\n", a.Registry.ActiveName())
		}

		assistantMsg := provider.Message{
			Role:      "assistant",
			Content:   fullResponse.String(),
			ToolCalls: toolCalls,
		}
		a.Session.AddMessage(assistantMsg)

		if len(toolCalls) == 0 {
			return nil
		}

		if err := a.handleToolCalls(ctx, toolCalls); err != nil {
			return err
		}
	}
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

	return nil
}
