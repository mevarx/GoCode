package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mevarx/GoCode/internal/hooks"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/pubsub"
	"github.com/mevarx/GoCode/internal/safetext"
	"github.com/mevarx/GoCode/internal/tools"
)

// LoopEventKind classifies what the agent loop is reporting.
type LoopEventKind int

const (
	// EventTurnStart reports that a turn began. It precedes every other event
	// of that turn, so a UI can arm timers before any provider output arrives.
	EventTurnStart LoopEventKind = iota
	// EventDelta is a fragment of assistant text for streaming to a display.
	EventDelta
	// EventToolStart reports a tool call before approval/execution so a UI can show pending state.
	EventToolStart
	// EventToolDelta reports streamed tool-call arguments before the call is
	// complete. It is display-only: approval and execution still wait for the
	// complete ToolCalls flush, so partial JSON can never be acted on.
	EventToolDelta
	// EventToolResult reports the outcome of a single tool call.
	EventToolResult
	// EventNotice is informational output that is not an error.
	EventNotice
	// EventError reports a failure the user should see.
	EventError
	// EventExit reports that a slash command asked the UI to shut down. The
	// UI renders any accompanying notice and then quits.
	EventExit
	// EventTurnEnd signals that the turn finished, successfully or not. It is
	// emitted exactly once per turn, on every exit path, and carries the
	// turn's metrics.
	EventTurnEnd
	// EventPermissionRequest opens a permission prompt. Must-deliver on
	// the bus; carries PermissionRequest. The UI transport stays the approval
	// bridge this milestone — bus observers get the payload in parallel.
	EventPermissionRequest
	// EventPermissionNotification reports a prompt lifecycle transition
	// (requested/granted/denied/cancelled, ). Must-deliver.
	EventPermissionNotification
	// EventPermissionModeChanged reports a permission mode switch.
	// Must-deliver.
	EventPermissionModeChanged
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
	// On EventToolDelta it carries the accumulated, sanitized argument preview
	// so far, so a card can update in place.
	Args string
	// Diff carries a unified diff on EventToolResult; kept separate from Text
	// so a UI can colour added/removed lines instead of parsing prose.
	Diff string
	// Err carries the underlying error on EventTurnEnd.
	Err error
	// Metrics carries the turn summary on EventTurnEnd; nil on every other
	// kind, so a zero TurnMetrics is never mistaken for a measurement.
	Metrics *TurnMetrics
	// PermissionRequest carries the prompt payload on EventPermissionRequest
	// nil on every other kind.
	PermissionRequest *pubsub.PermissionRequest
	// PermissionNotification carries the lifecycle payload on
	// EventPermissionNotification; nil on every other kind.
	PermissionNotification *pubsub.PermissionNotification
	// PermissionModeChanged carries the mode-switch payload on
	// EventPermissionModeChanged; nil on every other kind.
	PermissionModeChanged *pubsub.PermissionModeChanged
	// Cancelled marks a terminal EventTurnEnd for a turn that never entered
	// the stream: a cancel-on-entry settlement or a queued prompt dropped by
	// CancelTurns. A running turn cancelled mid-stream ends with
	// Err=context.Canceled but Cancelled=false — it ran and kept its partial
	// output. Front-ends use the flag to tell settlements from real turns.
	Cancelled bool
}

// maxDisplayedToolOutput caps rendered tool output; the full result still
// goes to the model and the session transcript.
const maxDisplayedToolOutput = 2000

// maxVerifyOutput caps the combined output of an auto-verify command, both in
// the notice and in the message handed back to the model.
const maxVerifyOutput = 8 * 1024

// DefaultVerifyTimeout bounds one auto-verify run when VerifyTimeout is unset.
const DefaultVerifyTimeout = 120 * time.Second

// readOnlyToolNames are built-in tools that never modify the workspace, so a
// round containing only these must not trigger auto-verify.
var readOnlyToolNames = map[string]bool{
	"file_read":   true,
	"code_search": true,
	"plan":        true,
	"scratchpad":  true,
}

// toolExecutionLimit bounds how many independent read-only calls a single
// assistant message may run at once.
const toolExecutionLimit = 4

// interruptedToolResult closes out a call a canceled turn never reached.
const interruptedToolResult = "Error: tool call interrupted before it could execute."

// SteeringNotice is emitted once for every drained steering batch. Front-ends
// key their queued-steering indicator off this exact text.
const SteeringNotice = "[Queued steering applied to the next request.]"

// steeringMarker prefixes a queued message so history shows why a user turn
// appeared mid-request. The model reads it as ordinary user guidance.
const steeringMarker = "[steering] "

// maxToolArgPreview bounds the accumulated argument preview on EventToolDelta,
// so a large streamed write cannot flood a front-end with repeated payloads.
const maxToolArgPreview = 4096

// AgentLoop is the single agent engine. Both the plain terminal loop and the
// TUI drive this same type: Run for the plain loop, RunTurn for the TUI.
type AgentLoop struct {
	Registry       *provider.Registry
	Session        *Session
	ToolRegistry   *tools.Registry
	Approval       *tools.ApprovalGate
	ContextManager *ContextManager
	WorkspaceRoot  string
	// Plan, when set, is rendered into the system prompt and updated by the
	// plan tool.
	Plan *tools.PlanStore
	// Scratchpad, when set, is the working-state store written by the
	// scratchpad tool. Its content is mirrored into the session and rendered
	// into the system prompt after each successful write.
	Scratchpad *tools.ScratchpadStore
	// VerifyCommand, when non-empty, runs once after any provider round that
	// executed a mutating tool. The user configured it explicitly, so it runs
	// without the approval gate, like shell_exec.
	VerifyCommand string
	// VerifyTimeout bounds each VerifyCommand run; zero or negative means
	// DefaultVerifyTimeout.
	VerifyTimeout time.Duration
	// GuardConfig carries the per-turn tool limits. Shared with the TUI path
	// so both loops stop under the same conditions.
	GuardConfig LoopGuardConfig
	// Observe, when set, receives every event the loop produces. Nil means
	// events are discarded, which is only appropriate in tests. Kept for
	// compatibility alongside Bus; removal is a later cleanup once all
	// consumers ride the bus.
	Observe func(LoopEvent)
	// Bus, when set, receives every event the loop produces in addition to
	// Observe, with delivery classes per kind (see loop_bus.go): deltas are
	// lossy frame-fidelity (no coalescing keys), terminal events are
	// must-deliver. Nil-safe: the plain terminal loop and tests run without
	// a bus. The bus never changes what Observe sees, and the exactly-once
	// EventTurnEnd funnel is shared.
	Bus *pubsub.Broker[LoopEvent]
	// AskApproval prompts for slash commands needing confirmation (e.g. /commit).
	// The plain loop supplies stdin backing; the TUI uses the approval gate instead.
	AskApproval func(prompt string) bool
	// Hooks, when set, runs the hook engine on tool calls, turn boundaries
	// and compaction. Nil means no hooks: every hook path is a nil check, so
	// an unconfigured loop pays zero overhead.
	Hooks *hooks.Runner
	// NonInteractive skips TURN hooks (headless parity for non-interactive
	// sessions); tool hooks still fire. Subagent-style nested
	// loops keep Hooks nil and fire nothing.
	NonInteractive bool
	// hookObserverInstalled guards the one-time PromptObserver chain install.
	hookObserverInstalled bool

	// steerMu guards the live-steering queue and the running-turn flag. Steer
	// is called from a front-end goroutine while a turn runs on another.
	steerMu     sync.Mutex
	steerQueue  []string
	turnRunning bool

	// dispatch is the per-session turn-entry state machine: accept
	// sequences, the cancel high-water mark, the active registration and the
	// FIFO queue of pending prompts. Zero-value ready; see dispatch.go.
	dispatch turnDispatcher
}

func NewAgentLoop(registry *provider.Registry, session *Session, toolReg *tools.Registry, approval *tools.ApprovalGate) *AgentLoop {
	a := &AgentLoop{
		Registry:       registry,
		Session:        session,
		ToolRegistry:   toolReg,
		Approval:       approval,
		ContextManager: NewContextManager(0),
	}
	// the gate publishes permission payloads through this loop onto the
	// bus, and remembers grants keyed to this loop's session. One gate belongs
	// to one loop; a gate shared by several loops binds to the last constructor.
	if approval != nil {
		approval.Bus = a
		if session != nil {
			approval.SetSessionID(session.ID())
		}
	}
	return a
}

func (a *AgentLoop) emit(ev LoopEvent) {
	if a.Observe != nil {
		a.Observe(ev)
	}
	if a.Bus != nil {
		a.publishBus(ev)
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

// Steer queues user guidance for the turn that is running. It is accepted
// only while a turn is in flight: the queue is drained at the next provider
// round boundary and never injected into a stream already in progress.
func (a *AgentLoop) Steer(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	if !a.turnRunning {
		return false
	}
	a.steerQueue = append(a.steerQueue, text)
	return true
}

// PendingSteering reports how many steering messages are queued for the
// running turn. It is observability for front-ends and tests; the queue is
// drained at the next provider round boundary.
func (a *AgentLoop) PendingSteering() int {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return len(a.steerQueue)
}

// beginTurn opens the steering window for a turn. A queue left over from a
// previous turn is discarded: it could no longer be applied to that turn.
func (a *AgentLoop) beginTurn() {
	a.steerMu.Lock()
	a.turnRunning = true
	a.steerQueue = nil
	a.steerMu.Unlock()
}

// endTurn closes the steering window. A steer that raced the last round
// boundary is dropped rather than leaking into the next turn.
func (a *AgentLoop) endTurn() {
	a.steerMu.Lock()
	a.turnRunning = false
	a.steerQueue = nil
	a.steerMu.Unlock()
}

// drainSteering applies queued steering messages as user messages, using the
// same session path normal input takes, and reports one notice per batch.
func (a *AgentLoop) drainSteering() int {
	a.steerMu.Lock()
	queued := a.steerQueue
	a.steerQueue = nil
	a.steerMu.Unlock()

	applied := 0
	for _, text := range queued {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		a.Session.AddMessage(provider.Message{Role: "user", Content: steeringMarker + text})
		applied++
	}
	if applied > 0 {
		a.emit(LoopEvent{Kind: EventNotice, Text: SteeringNotice})
	}
	return applied
}

// toolArgPreview sanitizes and bounds an accumulated argument buffer before it
// leaves the loop. Deltas are never logged raw.
func toolArgPreview(raw string) string {
	preview := safetext.Sanitize(raw)
	if len(preview) > maxToolArgPreview {
		// Do not split a multi-byte rune when cutting a long buffer.
		preview = strings.ToValidUTF8(preview[:maxToolArgPreview], "")
	}
	return preview
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

// EnsureSystemPrompt installs the prompt for the current run, replacing any
// prompt carried over from a resumed transcript. Workspace, branch and
// AGENTS.md context are per-run; a stored prompt would describe the old repo.
func (a *AgentLoop) EnsureSystemPrompt() {
	opts := DefaultPromptOptions(a.WorkspaceRoot, a.Registry.ActiveName(), a.Session.Model())
	if a.Plan != nil {
		opts.Plan = a.Plan.Render()
	}
	opts.Scratchpad = a.Session.Scratchpad()
	a.Session.SetSystemMessage(BuildSystemPromptWithOptions(opts))
}

// executeTurn runs one admitted turn: slash command or full agent turn with
// tools. Shared by the TUI and the plain loop through the dispatch entry
// (dispatch.go), so turn handling has one implementation. input is already
// trimmed and non-empty. A turn that runs reports EventTurnStart before
// anything else and exactly one EventTurnEnd, whatever its outcome.
func (a *AgentLoop) executeTurn(ctx context.Context, input string) (err error) {
	// "read-only tools this turn" grants never outlive the turn that set them.
	// Only an EXECUTING turn resets them: a queued or cancel-on-entry call
	// must never clear the grants of the turn that is actually running.
	if a.Approval != nil {
		a.Approval.BeginTurn()
		// The gate classifies danger and normalizes grant paths against the
		// loop's workspace; install it on every turn so a --workdir change
		// between turns is reflected.
		a.Approval.SetWorkspaceRoot(a.WorkspaceRoot)
	}
	// chain the permission-hook tap once, before any prompt can fire.
	a.installHookObserver()

	// Live steering is legal only while a turn runs. The window closes on
	// every exit path, including cancellation and slash commands.
	a.beginTurn()
	defer a.endTurn()

	// One record per turn, slash command or model turn: the profile must be
	// measured from before the work so compaction and retry deltas are right.
	m := newTurnMetrics(a)

	// modelTurn marks a turn that reaches the provider stream. Only those
	// turns fire turn hooks (only model turns fire them, which slash
	// commands never enter), and exactly one of TurnEnd/StopFailure/Interrupt
	// fires per finished model turn from this same funnel.
	modelTurn := false

	// The single exit funnel. Normal completion, cancellation, a loop-guard
	// stop, a provider failure and a slash command all end here, so no path can
	// miss the end event or emit it twice. A /exit turn emits EventExit just
	// before it, which is the one case where the end is not the last event a
	// front-end acts on.
	defer func() {
		// land every queued message before the end event so a cut-short
		// turn loses nothing.
		if a.Session != nil {
			a.Session.FlushMessages()
		}
		m.log()
		a.emit(LoopEvent{Kind: EventTurnEnd, Err: err, Metrics: m.snapshot()})
		if modelTurn {
			a.fireTurnEndHook(ctx, err)
		}
	}()

	a.emit(LoopEvent{Kind: EventTurnStart})

	if res := a.handleSlashCommand(ctx, input); res.Handled {
		m.slashCommand = true
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
	modelTurn = true
	a.fireTurnStartHook(ctx)
	return a.streamResponse(ctx, m)
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
		Hooks:          a.Hooks,
		Plan:           a.Plan,
	}
	return HandleCommand(ctx, cmdCtx, input)
}

// Runs provider round-trips until tools stop, bounded by the loop guard.
// The caller's metrics record is emitted when the turn ends, however it ends.
func (a *AgentLoop) streamResponse(ctx context.Context, m *turnMetrics) error {
	guard := NewLoopGuardWithConfig(a.GuardConfig)

	// m.log belongs to RunTurn's exit funnel, which runs exactly once.
	defer a.observeApprovals(m)()

	err := a.streamResponseGuarded(ctx, guard, m)
	// A hook halt is a clean termination (StopTurn maps onto end_turn):
	// the tool result already carries the halt wording, and the exit funnel
	// fires TurnEnd, not StopFailure.
	if errors.Is(err, errHookHalt) {
		return nil
	}
	return err
}

// observeApprovals chains the metrics observer onto the gate's decision hook,
// returning a restore func. Chaining rather than replacing keeps a caller's
// own observer working.
func (a *AgentLoop) observeApprovals(m *turnMetrics) func() {
	if a.Approval == nil {
		return func() {}
	}
	prev := a.Approval.OnDecision
	a.Approval.OnDecision = func(toolName string, approved, auto bool) {
		m.recordDecision(toolName, approved, auto)
		if prev != nil {
			prev(toolName, approved, auto)
		}
	}
	return func() { a.Approval.OnDecision = prev }
}

func (a *AgentLoop) streamResponseGuarded(ctx context.Context, guard *LoopGuard, m *turnMetrics) error {
	for {
		// Cancellation short-circuits before any queue drain: an aborted turn
		// must not absorb steering it will never send.
		if err := ctx.Err(); err != nil {
			return err
		}

		// A round boundary is the only place steering enters the request.
		// Draining here, before compaction and truncation, means the steered
		// text is accounted for by both.
		a.drainSteering()

		p, err := a.Registry.Active()
		if err != nil {
			return err
		}
		model := a.Session.Model()
		providerToolSpecs := a.toolSpecsAsProvider()

		// Resolve the budget each round so a /model switch takes effect on the
		// next request instead of at startup only.
		a.ContextManager.ResolveBudget(a.Registry, model)

		// Pre-request maintenance: automatic compaction runs before Truncate,
		// which stays the last resort and now reports what it dropped.
		a.autoCompactNow(ctx, false)

		// Truncation happens here, once, for every UI.
		history, droppedTurns := a.ContextManager.TruncateReport(a.Session.History())
		if droppedTurns > 0 && !m.truncationNoticed {
			m.truncationNoticed = true
			a.emit(LoopEvent{
				Kind: EventNotice,
				Text: fmt.Sprintf("[Context limit: %d earlier turns were dropped without summarization.]", droppedTurns),
			})
		}
		m.addPromptTokens(history)

		ch, err := a.openStream(ctx, p, model, providerToolSpecs, history, m)
		if err != nil {
			return err
		}

		var fullResponse strings.Builder
		var fullReasoning strings.Builder
		var toolCalls []provider.ToolCall
		var finishReason string
		// The last usage a provider reported for this request wins: Anthropic
		// sends cumulative counts, so later reports supersede earlier ones.
		var roundUsage provider.Usage
		var roundHasUsage bool
		// Per-call argument buffers for the delta preview, keyed by call id.
		toolArgBufs := make(map[string]*strings.Builder)

		for chunk := range ch {
			if chunk.Err != nil {
				return fmt.Errorf("stream chunk error: %w", chunk.Err)
			}

			if chunk.Usage != nil {
				roundUsage = *chunk.Usage
				roundHasUsage = true
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

			// Streamed arguments are display-only. They never enter approval
			// or execution: those still wait for the complete flush below.
			if d := chunk.ToolCallDelta; d != nil {
				id := d.ID
				if id == "" {
					// A dialect may deliver identity late; key the preview by
					// index so fragments still accumulate instead of colliding.
					id = fmt.Sprintf("tool_delta_%d", d.Index)
				}
				buf := toolArgBufs[id]
				if buf == nil {
					buf = &strings.Builder{}
					toolArgBufs[id] = buf
				}
				if d.ArgsDelta != "" {
					buf.WriteString(d.ArgsDelta)
				}
				a.emit(LoopEvent{
					Kind:       EventToolDelta,
					Tool:       d.Name,
					ToolCallID: id,
					Args:       toolArgPreview(buf.String()),
				})
			}

			// Honor cancellation promptly, so Ctrl+C and Esc stop a turn
			// instead of waiting for the provider to finish. Cancellation is
			// cooperative: whatever streamed so far is kept.
			if ctx.Err() != nil {
				a.keepPartialAssistant(fullResponse.String(), toolCalls, fullReasoning.String())
				return ctx.Err()
			}
		}

		if ctx.Err() != nil {
			a.keepPartialAssistant(fullResponse.String(), toolCalls, fullReasoning.String())
			return ctx.Err()
		}

		// Usage is recorded only for a request that ran to completion: a
		// cancelled stream reports partial counts that would corrupt the
		// context accounting.
		if roundHasUsage {
			m.addUsage(roundUsage)
		}
		m.addCompletion(fullResponse.String(), fullReasoning.String())
		if isTruncationFinish(finishReason) {
			m.truncations++
		}

		// A truncated stream that produced no text is explained by the
		// token-limit warning below, not by a credentials hint.
		if fullResponse.Len() == 0 && len(toolCalls) == 0 && !isTruncationFinish(finishReason) {
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
			// Post-turn maintenance: keep the window inside the compaction
			// threshold before the next user message arrives.
			a.autoCompactNow(ctx, false)
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

		if err := a.handleToolCallsWithVerify(ctx, toolCalls, m); err != nil {
			return err
		}
	}
}

// keepPartialAssistant persists the streamed portion of a cancelled turn
// the partial assistant message is kept
// instead of discarded, and every declared tool call gets a synthetic
// interrupted result so history stays paired. A turn cancelled before
// producing anything keeps nothing — an empty assistant row would be noise.
func (a *AgentLoop) keepPartialAssistant(content string, toolCalls []provider.ToolCall, reasoning string) {
	if content == "" && len(toolCalls) == 0 && reasoning == "" {
		return
	}
	a.Session.AddMessage(provider.Message{
		Role:             "assistant",
		Content:          content,
		ToolCalls:        toolCalls,
		ReasoningContent: reasoning,
	})
	if len(toolCalls) > 0 {
		a.writeSyntheticToolResults(toolCalls, interruptedToolResult)
	}
}

// handleToolCallsWithVerify executes a round's calls and runs the configured
// verify command at most once if any executed tool can mutate the workspace.
func (a *AgentLoop) handleToolCallsWithVerify(ctx context.Context, toolCalls []provider.ToolCall, m *turnMetrics) error {
	var mutated bool
	restore := a.observeMutations(&mutated)
	err := a.handleToolCalls(ctx, toolCalls, m)
	restore()
	if err != nil {
		return err
	}
	if mutated && strings.TrimSpace(a.VerifyCommand) != "" {
		a.runAutoVerify(ctx)
	}
	return nil
}

// observeMutations chains onto the gate's decision hook, recording whether
// any approved call in the round uses a tool that can modify the workspace.
// Denied calls are not approved and therefore do not count.
func (a *AgentLoop) observeMutations(mutated *bool) func() {
	if a.Approval == nil {
		return func() {}
	}
	prev := a.Approval.OnDecision
	a.Approval.OnDecision = func(toolName string, approved, auto bool) {
		if approved && a.toolCanMutate(toolName) {
			*mutated = true
		}
		if prev != nil {
			prev(toolName, approved, auto)
		}
	}
	return func() { a.Approval.OnDecision = prev }
}

// toolCanMutate reports whether a tool may modify the workspace. Built-in
// read-only tools are excluded by name, and any tool that opts into the
// read-only contract is excluded by interface.
func (a *AgentLoop) toolCanMutate(toolName string) bool {
	if readOnlyToolNames[toolName] {
		return false
	}
	tool := a.ToolRegistry.Get(toolName)
	if tool == nil {
		return false
	}
	if ro, ok := tool.(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
		return false
	}
	return true
}

// runAutoVerify runs the user-configured verify command once in the workspace.
// It deliberately skips the approval gate: configuring the command is the
// opt-in, exactly as with an auto-approved shell_exec. A failure is reported
// to the UI and appended to history so the next provider round can see it.
func (a *AgentLoop) runAutoVerify(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	command := strings.TrimSpace(a.VerifyCommand)
	timeout := a.VerifyTimeout
	if timeout <= 0 {
		timeout = DefaultVerifyTimeout
	}

	vctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(vctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(vctx, "sh", "-c", command)
	}
	if a.WorkspaceRoot != "" {
		cmd.Dir = a.WorkspaceRoot
	}

	out := &cappedBuffer{limit: maxVerifyOutput}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	output := strings.TrimSpace(out.String())
	if out.truncated {
		output += "\n… (truncated)"
	}
	// Match the shell_exec transcript boundary: credentials in command output
	// never reach the model or the persisted session.
	output, _ = tools.RedactToolOutput(output)

	if err == nil {
		a.emit(LoopEvent{Kind: EventNotice, Text: fmt.Sprintf("[auto-verify] %s passed", command)})
		return
	}

	code := exitCode(err)
	timedOut := errors.Is(vctx.Err(), context.DeadlineExceeded)
	notice := fmt.Sprintf("[auto-verify] %s failed (exit %d)", command, code)
	if timedOut {
		notice += fmt.Sprintf(" (timed out after %s)", timeout)
	}
	if output != "" {
		notice += "\n" + output
	}
	a.emit(LoopEvent{Kind: EventNotice, Text: notice})

	message := fmt.Sprintf("[auto-verify] Command failed (exit %d): %s", code, command)
	if timedOut {
		message += fmt.Sprintf(" (timed out after %s)", timeout)
	}
	if output != "" {
		message += "\n" + output
	}
	a.Session.AddMessage(provider.Message{Role: "user", Content: message})
}

// exitCode maps a failed command to an exit code, using -1 when the process
// never produced one (start failure or signal).
func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// cappedBuffer keeps up to limit bytes and discards the rest, so a chatty
// verify command cannot balloon the transcript. It is safe for concurrent
// writes because os/exec may copy stdout and stderr on separate goroutines.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	// Always report a full write: a short write makes os/exec stop copying.
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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

func (a *AgentLoop) handleToolCalls(ctx context.Context, toolCalls []provider.ToolCall, m *turnMetrics) error {
	// Phase A is sequential and in call order: announce every call and run the
	// approval gate for it. Approval prompts therefore appear deterministically,
	// before any tool has produced output, even when execution is concurrent.
	entries := make([]*preparedToolCall, 0, len(toolCalls))
	for i, tc := range toolCalls {
		if ctx.Err() != nil {
			// Close out prepared calls that will never run, then every call
			// this batch never reached.
			for _, e := range entries {
				if !e.executed {
					e.interrupt()
				}
			}
			a.finishToolCalls(ctx, entries, m)
			a.writeSyntheticToolResults(toolCalls[i:], interruptedToolResult)
			return ctx.Err()
		}

		start := m.beginTool()

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
			// No approval and no execution: the error result is final.
			entries = append(entries, &preparedToolCall{call: tc, unknown: true})
			continue
		}

		entry := &preparedToolCall{
			call:         tc,
			tool:         tool,
			started:      start,
			countLatency: true,
			readOnly:     a.toolIsReadOnly(tool, tc.Name),
		}

		// PreToolUse: block, rewrite or approve BEFORE the permission
		// gate. A deny blocks this one call (the model may retry); a halt
		// ends the turn after the batch's results are published. An allow
		// stamps a per-call approval the gate consumes once. The deny-list
		// inside the gate still outranks a hook allow.
		prepCtx := ctx
		finalArgs := json.RawMessage(tc.Args)
		if a.Hooks.Has(hooks.PreToolUse) {
			in := a.hookInput()
			in.ToolName = tc.Name
			in.ToolInput = hooks.ParseToolInput(tc.Args)
			in.ToolUseID = tc.ID
			pre := a.Hooks.Run(ctx, hooks.PreToolUse, in)
			a.emitHookDiagnostics(hooks.PreToolUse, pre)
			if pre.Decision == hooks.DecisionDeny || pre.Halt {
				reason := pre.Reason
				wording := "Tool call blocked by hook. Reason: %s"
				if pre.Halt {
					wording = "Turn halted by hook. Reason: %s"
					if reason == "" {
						reason = "turn halted by hook"
					}
				} else if reason == "" {
					reason = "blocked by hook"
				}
				entry.result = tools.Result{Error: fmt.Sprintf(wording, reason)}
				entry.blocked = true
				entries = append(entries, entry)
				if pre.Halt {
					// Close out prepared calls, publish the batch, and give
					// every unreached call a result so history stays paired.
					for _, e := range entries {
						e.interrupt()
					}
					a.finishToolCalls(ctx, entries, m)
					a.writeSyntheticToolResults(toolCalls[i+1:], haltedToolResult)
					return errHookHalt
				}
				continue
			}
			if len(pre.UpdatedInput) > 0 {
				finalArgs = pre.UpdatedInput
				entry.call.Args = finalArgs
			}
			if pre.Decision == hooks.DecisionAllow && a.Approval != nil {
				a.Approval.GrantHookApproval(tc.ID)
				prepCtx = tools.WithHookApproval(ctx, tc.ID)
			}
			entry.hookContext = pre.Context
		}

		execute, err := a.Approval.PrepareWithID(prepCtx, tool, finalArgs, tc.ID)
		if err != nil {
			entry.err = err
			entry.duration = time.Since(start)
		} else {
			entry.execute = execute
		}
		entries = append(entries, entry)
	}

	// Phase B executes prepared calls. Read-only calls may run concurrently
	// (bounded), while mutating calls stay serialized and never overlap a
	// read-only call, so no tool observes a half-written workspace.
	if err := a.executeToolCalls(ctx, entries); err != nil {
		a.finishToolCalls(ctx, entries, m)
		return err
	}

	// Phase C publishes results in call order, regardless of completion order.
	// A post-hook halt ends the turn once every result is in the transcript.
	if halted := a.finishToolCalls(ctx, entries, m); halted {
		return errHookHalt
	}
	return nil
}

// preparedToolCall is one call from an assistant message after Phase A. The
// gate has already decided whether it may run; the execute closure runs the
// tool with the final (possibly edited, possibly granted) arguments.
type preparedToolCall struct {
	call         provider.ToolCall
	tool         tools.Tool
	execute      func(context.Context) (tools.Result, error)
	readOnly     bool
	started      time.Time
	countLatency bool
	duration     time.Duration
	executed     bool
	interrupted  bool
	unknown      bool
	// blocked marks a call a PreToolUse hook denied: its result is final and
	// the inner tool never runs.
	blocked bool
	// hookContext is PreToolUse context appended to the result text.
	hookContext string
	result      tools.Result
	err         error
}

// interrupt finalizes a call the turn never reached. It stays in the
// transcript as a tool result so history has no dangling call ids. A blocked
// call already carries its final hook-denial result and is left alone.
func (p *preparedToolCall) interrupt() {
	if p.executed || p.interrupted || p.blocked {
		return
	}
	p.interrupted = true
	p.execute = nil
	p.result = tools.Result{}
	p.err = nil
}

// toolIsReadOnly reports whether a call may run concurrently with other
// read-only calls. Tools that opt into the read-only contract decide for
// themselves (MCP servers report the readOnlyHint); the built-in non-prompting
// readers are classified by name. This is a concurrency classification only;
// approval behavior is unchanged.
func (a *AgentLoop) toolIsReadOnly(tool tools.Tool, name string) bool {
	if ro, ok := tool.(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
		return true
	}
	return readOnlyToolNames[name]
}

// executeToolCalls walks prepared calls in original order. Read-only calls are
// dispatched to a bounded worker set; a mutating call waits for the read-only
// calls dispatched before it, then runs alone. That keeps call-order effects
// (and cancellation) predictable without serializing independent reads.
func (a *AgentLoop) executeToolCalls(ctx context.Context, entries []*preparedToolCall) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, toolExecutionLimit)

	waitPending := func() { wg.Wait() }

	for _, e := range entries {
		if ctx.Err() != nil && !e.executed && !e.interrupted {
			// Unreached by the canceled turn; keep the message for Phase C.
			e.interrupt()
			continue
		}
		if e.execute == nil {
			// Unknown tool or prepare error: the result is already final.
			continue
		}
		if e.readOnly {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				e.interrupt()
				continue
			}
			wg.Add(1)
			go func(e *preparedToolCall) {
				defer wg.Done()
				defer func() { <-sem }()
				a.runPreparedCall(ctx, e)
			}(e)
			continue
		}

		// A mutating call never overlaps the reads dispatched before it.
		waitPending()
		if ctx.Err() != nil {
			e.interrupt()
			continue
		}
		a.runPreparedCall(ctx, e)
	}
	waitPending()

	for _, e := range entries {
		if e.interrupted {
			return ctx.Err()
		}
	}
	return nil
}

// runPreparedCall executes one prepared call and records its measured
// duration; the duration is added to metrics later, sequentially.
func (a *AgentLoop) runPreparedCall(ctx context.Context, e *preparedToolCall) {
	if e.executed || e.interrupted {
		return
	}
	if ctx.Err() != nil {
		e.interrupt()
		return
	}
	e.result, e.err = e.execute(ctx)
	e.executed = true
	e.duration = time.Since(e.started)
}

// recordToolLatency adds one call's measured duration. Concurrent execution
// measures in its own goroutine; the duration is added here from the
// sequential result phase, so the metrics struct needs no lock.
func recordToolLatency(m *turnMetrics, name string, d time.Duration) {
	if name == "" {
		return
	}
	m.toolLatency[name] += d
}

// finishToolCalls appends each result to the session and emits the matching
// event in call order. It is the single transcript boundary for a batch, so
// concurrent execution cannot reorder history or the UI stream. post hooks
// fire here for executed calls only (a blocked call never ran); their context
// and feedback ride the same result text the model AND the user see. It
// reports whether a post hook halted the turn.
func (a *AgentLoop) finishToolCalls(ctx context.Context, entries []*preparedToolCall, m *turnMetrics) (halted bool) {
	for _, e := range entries {
		tc := e.call

		if e.interrupted {
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    interruptedToolResult,
				ToolCallID: tc.ID,
			})
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, ToolCallID: tc.ID, Text: interruptedToolResult, IsError: true})
			continue
		}

		if e.unknown {
			msg := fmt.Sprintf("Error: unknown tool %q", tc.Name)
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    msg,
				ToolCallID: tc.ID,
			})
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, ToolCallID: tc.ID, Text: msg, IsError: true})
			continue
		}

		if e.countLatency {
			recordToolLatency(m, tc.Name, e.duration)
		}

		// PostToolUse / PostToolUseFailure: success vs failure is the Go
		// error OR an error result. A cancelled turn skips post hooks (its
		// Interrupt hook covers the boundary); a post block cannot undo the
		// call, so its reason becomes "Hook feedback: …" on the result.
		var post *hooks.Aggregated
		if e.executed && ctx.Err() == nil {
			postEvent := hooks.PostToolUse
			if e.err != nil || e.result.Error != "" {
				postEvent = hooks.PostToolUseFailure
			}
			if a.Hooks.Has(postEvent) {
				agg := a.firePostToolHook(ctx, e, postEvent)
				post = &agg
				if agg.Halt {
					halted = true
				}
			}
		}

		if e.err != nil {
			msg := fmt.Sprintf("Error executing %s: %v", tc.Name, e.err)
			if extra := hookExtraText(e.hookContext, post); extra != "" {
				msg += "\n" + extra
			}
			a.Session.AddMessage(provider.Message{
				Role:       "tool",
				Content:    msg,
				ToolCallID: tc.ID,
			})
			a.emit(LoopEvent{Kind: EventToolResult, Tool: tc.Name, ToolCallID: tc.ID, Text: msg, IsError: true})
			continue
		}

		// Hook output rewrite and text additions land BEFORE the redaction
		// boundary, so every result — hook text included — crosses it exactly
		// once and no configuration can turn masking off.
		result := applyHookOutput(e.result, post)
		if extra := hookExtraText(e.hookContext, post); extra != "" {
			result = appendHookExtra(result, extra)
		}
		result, _ = tools.RedactToolResult(result, nil)
		content := result.String()
		if result.Diff != "" {
			content = fmt.Sprintf("%s\n\nDiff:\n%s", content, result.Diff)
		}

		a.Session.AddMessage(provider.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: tc.ID,
		})

		// The plan and scratchpad are part of the system prompt; rebuild it so
		// the next provider round sees the state the model just wrote.
		if _, ok := e.tool.(*tools.PlanTool); ok && result.Error == "" && a.Plan != nil {
			a.EnsureSystemPrompt()
		}
		if _, ok := e.tool.(*tools.ScratchpadTool); ok && result.Error == "" && a.Scratchpad != nil {
			a.Session.SetScratchpad(a.Scratchpad.Render())
			a.EnsureSystemPrompt()
		}

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
	return halted
}
