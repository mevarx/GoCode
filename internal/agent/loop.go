package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// LoopEventKind classifies what the agent loop is reporting.
type LoopEventKind int

const (
	// EventDelta is a fragment of assistant text, suitable for streaming to
	// a display.
	EventDelta LoopEventKind = iota
	// EventToolResult reports the outcome of a single tool call.
	EventToolResult
	// EventNotice is informational output that is not an error, such as the
	// tool-usage summary or a slash command's result.
	EventNotice
	// EventError reports a failure the user should see.
	EventError
	// EventExit reports that a slash command asked the UI to shut down. The
	// UI renders any accompanying notice and then quits.
	EventExit
	// EventTurnEnd signals that the turn finished, successfully or not.
	EventTurnEnd
)

// LoopEvent is a single thing the agent loop wants the UI to show.
//
// The loop does not know whether it is driving a plain terminal or a Bubble
// Tea program, so it reports rather than prints. There is exactly one engine;
// each UI supplies a renderer for these events.
type LoopEvent struct {
	Kind    LoopEventKind
	Text    string // delta text, notice body, or error text
	Tool    string // tool name for EventToolResult
	IsError bool
	// Err carries the underlying error on EventTurnEnd.
	Err error
}

// maxDisplayedToolOutput caps how much of a tool result is rendered. The full
// result always goes to the model and the session transcript; this only limits
// what the user sees.
const maxDisplayedToolOutput = 2000

// AgentLoop is the single agent engine. Both the plain terminal loop and the
// TUI drive this same type: Run for the plain loop, RunTurn for the TUI.
type AgentLoop struct {
	Registry       *provider.Registry
	Session        *Session
	ToolRegistry   *tools.Registry
	Approval       *tools.ApprovalGate
	ContextManager *ContextManager
	WorkspaceRoot  string
	// GuardConfig carries the per-turn tool limits. Shared with the TUI path
	// so both loops stop under the same conditions.
	GuardConfig LoopGuardConfig
	// Observe, when set, receives every event the loop produces. Nil means
	// events are discarded, which is only appropriate in tests.
	Observe func(LoopEvent)
	// AskApproval, when set, is used by slash commands that need to prompt
	// the user (for example /commit). The plain terminal loop supplies a
	// stdin-backed implementation; the TUI relies on the approval gate's
	// OnPresent instead and leaves this nil.
	AskApproval func(prompt string) bool
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

// emit reports an event to the observer, if one is attached.
func (a *AgentLoop) emit(ev LoopEvent) {
	if a.Observe != nil {
		a.Observe(ev)
	}
}

func (a *AgentLoop) emitDelta(text string) {
	a.emit(LoopEvent{Kind: EventDelta, Text: text})
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

// EnsureSystemPrompt adds the system prompt if the session has none.
func (a *AgentLoop) EnsureSystemPrompt() {
	for _, m := range a.Session.History() {
		if m.Role == "system" {
			return
		}
	}
	opts := DefaultPromptOptions(a.WorkspaceRoot, a.Registry.ActiveName(), a.Session.Model())
	a.Session.AddMessage(provider.Message{
		Role:    "system",
		Content: BuildSystemPromptWithOptions(opts),
	})
}

// RunTurn processes one user input line: a slash command if it is one,
// otherwise a full agent turn including any tool calls.
//
// This is the entry point the TUI uses. The plain loop in Run calls it too,
// so there is one implementation of turn handling in the codebase.
func (a *AgentLoop) RunTurn(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	if res := a.handleSlashCommand(ctx, input); res.Handled {
		if res.Exit {
			if res.Output != "" {
				a.emit(LoopEvent{Kind: EventExit, Text: res.Output})
			} else {
				a.emit(LoopEvent{Kind: EventExit})
			}
			return errExitRequested
		}
		if res.Error != nil {
			a.emit(LoopEvent{Kind: EventError, Text: res.Error.Error()})
		} else if res.Output != "" {
			a.emit(LoopEvent{Kind: EventNotice, Text: res.Output})
		}
		return nil
	}

	a.Session.AddMessage(provider.Message{Role: "user", Content: input})
	return a.streamResponse(ctx)
}

// errExitRequested signals that a slash command asked the UI to quit.
var errExitRequested = fmt.Errorf("exit requested")

// IsExitRequest reports whether err came from a /exit-style command.
func IsExitRequest(err error) bool {
	return err == errExitRequested
}

func (a *AgentLoop) handleSlashCommand(ctx context.Context, input string) CommandResult {
	ask := a.AskApproval
	if ask == nil {
		ask = func(string) bool { return false }
	}
	cmdCtx := CommandContext{
		Session:        a.Session,
		Registry:       a.Registry,
		ContextManager: a.ContextManager,
		WorkspaceRoot:  a.WorkspaceRoot,
		AskApproval:    ask,
	}
	return HandleCommand(ctx, cmdCtx, input)
}

// streamResponse runs a full agent turn: repeated provider round-trips until
// the model stops requesting tools, bounded by the loop guard.
func (a *AgentLoop) streamResponse(ctx context.Context) error {
	guard := NewLoopGuardWithConfig(a.GuardConfig)
	return a.streamResponseGuarded(ctx, guard)
}

func (a *AgentLoop) streamResponseGuarded(ctx context.Context, guard *LoopGuard) error {
	for {
		p, err := a.Registry.Active()
		if err != nil {
			return err
		}
		model := a.Session.Model()
		providerToolSpecs := a.toolSpecsAsProvider()

		// Truncation happens here, once, for every UI.
		history := a.ContextManager.Truncate(a.Session.History())

		ch, err := p.Stream(ctx, model, history, providerToolSpecs)
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		var fullResponse strings.Builder
		var fullReasoning strings.Builder
		var toolCalls []provider.ToolCall

		for chunk := range ch {
			if chunk.Err != nil {
				return fmt.Errorf("stream chunk error: %w", chunk.Err)
			}

			if chunk.Delta != "" {
				a.emitDelta(chunk.Delta)
				fullResponse.WriteString(chunk.Delta)
			}

			// Reasoning is captured but not displayed: it is model-internal
			// thinking that some providers require to be replayed on the next
			// turn, so it belongs in history without cluttering the terminal.
			if chunk.Reasoning != "" {
				fullReasoning.WriteString(chunk.Reasoning)
			}

			if len(chunk.ToolCalls) > 0 {
				toolCalls = append(toolCalls, chunk.ToolCalls...)
			}

			// Honor cancellation promptly, so Ctrl+C and Esc stop a turn
			// instead of waiting for the provider to finish.
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if fullResponse.Len() == 0 && len(toolCalls) == 0 {
			a.emit(LoopEvent{
				Kind: EventNotice,
				Text: fmt.Sprintf("[No response received from provider %q. Verify provider API keys/credentials or switch with /provider]", a.Registry.ActiveName()),
			})
		}

		a.Session.AddMessage(provider.Message{
			Role:             "assistant",
			Content:          fullResponse.String(),
			ToolCalls:        toolCalls,
			ReasoningContent: fullReasoning.String(),
		})

		if len(toolCalls) == 0 {
			if s := guard.Summary(); s != "" {
				a.emit(LoopEvent{Kind: EventNotice, Text: s})
			}
			return nil
		}

		// Bound the turn before executing anything. A failing tool that the
		// model retries identically must still hit the cap, so the check
		// happens here rather than after execution.
		if err := guard.CheckCall(toolCalls); err != nil {
			a.Session.AddMessage(provider.Message{
				Role:    "assistant",
				Content: fmt.Sprintf("Stopped: %v", err),
			})
			a.emit(LoopEvent{Kind: EventError, Text: err.Error()})
			return nil
		}

		if err := a.handleToolCalls(ctx, toolCalls); err != nil {
			return err
		}
	}
}

func (a *AgentLoop) handleToolCalls(ctx context.Context, toolCalls []provider.ToolCall) error {
	for _, tc := range toolCalls {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		tool := a.ToolRegistry.Get(tc.Name)
		if tool == nil {
			msg := fmt.Sprintf("Error: unknown tool %q", tc.Name)
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    msg,
				ToolCallID: tc.ID,
			})
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, Text: msg, IsError: true})
			continue
		}

		result, err := a.Approval.WrapExecution(ctx, tool, json.RawMessage(tc.Args))
		if err != nil {
			msg := fmt.Sprintf("Error executing %s: %v", tc.Name, err)
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    msg,
				ToolCallID: tc.ID,
			})
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, Text: msg, IsError: true})
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

		display := result.Output
		if result.Error != "" {
			display = result.Error
		}
		if len(display) > maxDisplayedToolOutput {
			display = display[:maxDisplayedToolOutput] + "\n… (truncated)"
		}
		a.emit(LoopEvent{
			Kind:    EventToolResult,
			Tool:    tc.Name,
			Text:    display,
			IsError: result.Error != "",
		})
	}

	return nil
}
