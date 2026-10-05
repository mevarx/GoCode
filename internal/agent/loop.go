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
	// EventDelta is a fragment of assistant text for streaming to a display.
	EventDelta LoopEventKind = iota
	// EventToolStart reports a tool call before approval/execution so a UI can show pending state.
	EventToolStart
	// EventToolResult reports the outcome of a single tool call.
	EventToolResult
	// EventNotice is informational output that is not an error.
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
// The loop reports rather than prints; each UI supplies a renderer.
type LoopEvent struct {
	Kind    LoopEventKind
	Text    string // delta text, notice body, or error text
	Tool    string // tool name for EventToolStart and EventToolResult
	IsError bool
	// ToolCallID correlates start/result for the same call so a UI cannot attribute
	// a result to the wrong call when several run in one batch.
	ToolCallID string
	// Args carries raw JSON arguments on EventToolStart so a card can show the
	// request without waiting for execution; the UI decides how much to show.
	Args string
	// Diff carries a unified diff on EventToolResult; kept separate from Text
	// so a UI can colour added/removed lines instead of parsing prose.
	Diff string
	// Err carries the underlying error on EventTurnEnd.
	Err error
}

// maxDisplayedToolOutput caps rendered tool output; the full result still
// goes to the model and the session transcript.
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
	// AskApproval prompts for slash commands needing confirmation (e.g. /commit).
	// The plain loop supplies stdin backing; the TUI uses the approval gate instead.
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

func (a *AgentLoop) emit(ev LoopEvent) {
	if a.Observe != nil {
		a.Observe(ev)
	}
}

// Reports token-budget truncation ("length"/"max_tokens").
func isTruncationFinish(reason string) bool {
	switch reason {
	case "length", "max_tokens":
		return true
	}
	return false
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

// RunTurn processes one user input: slash command or full agent turn with tools.
// Shared by the TUI and the plain loop, so turn handling has one implementation.
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

// Runs provider round-trips until tools stop, bounded by the loop guard.
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
		var finishReason string

		for chunk := range ch {
			if chunk.Err != nil {
				return fmt.Errorf("stream chunk error: %w", chunk.Err)
			}

			if chunk.Delta != "" {
				a.emitDelta(chunk.Delta)
				fullResponse.WriteString(chunk.Delta)
			}

			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
			}

			// Reasoning is model-internal thinking some providers require replayed,
			// so it belongs in history without cluttering the terminal.
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
			if isTruncationFinish(finishReason) {
				a.emit(LoopEvent{
					Kind: EventNotice,
					Text: fmt.Sprintf("[Warning: the provider stopped at the token limit (finish_reason %q); the answer may be incomplete.]", finishReason),
				})
			}
			return nil
		}

		// Bound the turn before executing: a failing tool retried identically
		// must still hit the cap, so the check precedes execution.
		if err := guard.CheckCall(toolCalls); err != nil {
			// Calls are already in the session, so each needs a result: both
			// OpenAI-shaped and Anthropic APIs reject calls without results.
			a.writeSyntheticToolResults(toolCalls, fmt.Sprintf("Error: tool call not executed: %v", err))
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

// writeSyntheticToolResults closes out calls that never produced a result.
// Required because OpenAI-shaped and Anthropic APIs reject history with a call lacking a result.
func (a *AgentLoop) writeSyntheticToolResults(toolCalls []provider.ToolCall, reason string) {
	for _, tc := range toolCalls {
		a.Session.AddMessage(provider.Message{
			Role:       "tool",
			Content:    reason,
			ToolCallID: tc.ID,
		})
	}
}

func (a *AgentLoop) handleToolCalls(ctx context.Context, toolCalls []provider.ToolCall) error {
	for i, tc := range toolCalls {
		if ctx.Err() != nil {
			// Close out unreached calls; history must not keep a call without a result.
			a.writeSyntheticToolResults(toolCalls[i:], "Error: tool call interrupted before it could execute.")
			return ctx.Err()
		}

		// Announce before anything can block: the approval gate may wait on the
		// user, and the UI would otherwise show nothing while approval pends.
		a.emit(LoopEvent{
			Kind:       EventToolStart,
			Tool:       tc.Name,
			ToolCallID: tc.ID,
			Args:       string(tc.Args),
		})

		tool := a.ToolRegistry.Get(tc.Name)
		if tool == nil {
			msg := fmt.Sprintf("Error: unknown tool %q", tc.Name)
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    msg,
				ToolCallID: tc.ID,
			})
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, ToolCallID: tc.ID, Text: msg, IsError: true})
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
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, ToolCallID: tc.ID, Text: msg, IsError: true})
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
			Kind:       EventToolResult,
			Tool:       tc.Name,
			ToolCallID: tc.ID,
			Text:       display,
			Diff:       result.Diff,
			IsError:    result.Error != "",
		})
	}

	return nil
}
