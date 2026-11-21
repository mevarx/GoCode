package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/hooks"
	"github.com/mevarx/GoCode/internal/pubsub"
	"github.com/mevarx/GoCode/internal/safetext"
	"github.com/mevarx/GoCode/internal/tools"
)

// hook wiring. The engine itself lives in
// internal/hooks; this file connects it to the loop's existing seams:
//
// - PreToolUse before approval (block/rewrite/per-call allow), PostToolUse /
// PostToolUseFailure after execution, in the single transcript boundary.
// - TurnStart before the stream; exactly ONE of TurnEnd / StopFailure /
// Interrupt per finished model turn, fired from RunTurn's exit funnel.
// - PermissionRequest / PermissionResult informational hooks ride 's
// PromptObserver tap, off the requesting goroutine.
// - PreCompact / PostCompact wrap the compaction summary request.
//
// Headless parity: NonInteractive skips TURN hooks only; tool hooks still
// fire. Slash-command turns never reach the stream and fire no turn hooks
// (only model turns fire them, which commands do not enter). A future
// subagent-style nested loop keeps Hooks nil (or NonInteractive) and fires
// nothing. With no hooks configured every path below is a nil check: zero
// overhead.

// errHookHalt ends the turn cleanly after a hook halt. A halt is a normal
// termination (StopTurn maps onto end_turn), so streamResponse folds
// this sentinel into a nil return and the exit funnel fires TurnEnd.
var errHookHalt = errors.New("turn halted by hook")

// haltedToolResult closes out calls a halted turn never reached.
const haltedToolResult = "Error: tool call not executed: turn halted by hook."

// hookInput builds the common payload fields from the loop's session and
// workspace. Callers add the event-specific fields.
func (a *AgentLoop) hookInput() hooks.Input {
	in := hooks.Input{CWD: a.WorkspaceRoot}
	if a.Session != nil {
		in.SessionID = a.Session.ID()
	}
	return in
}

// emitHookDiagnostics surfaces abnormal hook outcomes (timeouts, abandons,
// error exits) as one sanitized notice. The engine returns diagnostics
// instead of logging; the loop decides how to show them.
func (a *AgentLoop) emitHookDiagnostics(event hooks.Event, agg hooks.Aggregated) {
	if len(agg.Diagnostics) == 0 {
		return
	}
	text := fmt.Sprintf("[hooks] %s: %s", event, strings.Join(agg.Diagnostics, "; "))
	a.emit(LoopEvent{Kind: EventNotice, Text: safetext.Sanitize(text)})
}

// turnHooksEnabled reports whether TURN hooks may fire. Headless
// (NonInteractive) skips them; tool hooks are unaffected.
func (a *AgentLoop) turnHooksEnabled() bool {
	return a.Hooks != nil && !a.NonInteractive
}

// fireTurnStartHook runs TurnStart before the provider stream.
func (a *AgentLoop) fireTurnStartHook(ctx context.Context) {
	if !a.turnHooksEnabled() || !a.Hooks.Has(hooks.TurnStart) {
		return
	}
	agg := a.Hooks.Run(ctx, hooks.TurnStart, a.hookInput())
	a.emitHookDiagnostics(hooks.TurnStart, agg)
}

// fireTurnEndHook fires exactly one of TurnEnd / StopFailure / Interrupt for
// a finished model turn, from RunTurn's single exit funnel. The turn context
// may already be cancelled (Interrupt) or spent, so the firing rides a
// detached context with the engine's own per-hook budgets.
func (a *AgentLoop) fireTurnEndHook(ctx context.Context, turnErr error) {
	if !a.turnHooksEnabled() {
		return
	}
	event := hooks.TurnEnd
	in := a.hookInput()
	switch {
	case errors.Is(turnErr, context.Canceled), errors.Is(turnErr, context.DeadlineExceeded):
		event = hooks.Interrupt
	case turnErr != nil:
		event = hooks.StopFailure
	default:
		if a.Session != nil {
			in.LastAssistantMessage = a.Session.LastAssistantMessage()
		}
	}
	if !a.Hooks.Has(event) {
		return
	}
	agg := a.Hooks.Run(context.WithoutCancel(ctx), event, in)
	a.emitHookDiagnostics(event, agg)
}

// installHookObserver chains the permission-hook tap onto 's PromptObserver
// once, preserving any observer a caller already installed. The gate runs
// observers off the requesting goroutine, so a slow hook never delays a tool.
func (a *AgentLoop) installHookObserver() {
	if a.Hooks == nil || a.Approval == nil || a.hookObserverInstalled {
		return
	}
	prev := a.Approval.Observer
	a.Approval.Observer = &hookPromptObserver{loop: a, prev: prev}
	a.hookObserverInstalled = true
}

// hookPromptObserver fires the informational permission hooks.
// Their decisions are ignored: only the prompt itself grants or denies.
type hookPromptObserver struct {
	loop *AgentLoop
	prev tools.PromptObserver
}

func (o *hookPromptObserver) PromptShown(req pubsub.PermissionRequest) {
	o.loop.firePermissionHook(hooks.PermissionRequest, req, "")
	if o.prev != nil {
		o.prev.PromptShown(req)
	}
}

func (o *hookPromptObserver) PromptResolved(req pubsub.PermissionRequest, outcome tools.PromptOutcome) {
	o.loop.firePermissionHook(hooks.PermissionResult, req, outcome.String())
	if o.prev != nil {
		o.prev.PromptResolved(req, outcome)
	}
}

// firePermissionHook runs one informational permission hook. It uses a
// background context: the tap may run after the turn context is done, and an
// audit hook must still fire.
func (a *AgentLoop) firePermissionHook(event hooks.Event, req pubsub.PermissionRequest, decision string) {
	if a.Hooks == nil || !a.Hooks.Has(event) {
		return
	}
	in := a.hookInput()
	in.ToolName = req.Tool
	in.ToolInput = hooks.ParseToolInput(req.Args)
	in.ToolUseID = req.ToolCallID
	in.Decision = decision
	agg := a.Hooks.Run(context.Background(), event, in)
	a.emitHookDiagnostics(event, agg)
}

// firePostToolHook runs PostToolUse (success) or PostToolUseFailure (Go error
// or an error result) for one executed call. A post hook cannot undo the
// call; its effects are output rewrite, context and feedback text, and halt.
func (a *AgentLoop) firePostToolHook(ctx context.Context, e *preparedToolCall, event hooks.Event) hooks.Aggregated {
	in := a.hookInput()
	in.ToolName = e.call.Name
	in.ToolInput = hooks.ParseToolInput(e.call.Args)
	in.ToolUseID = e.call.ID
	in.ToolResponse = e.result.String()
	if e.err != nil {
		in.Error = e.err.Error()
	} else if e.result.Error != "" {
		in.Error = e.result.Error
	}
	agg := a.Hooks.Run(ctx, event, in)
	a.emitHookDiagnostics(event, agg)
	return agg
}

// hookExtraText collects the text a hook firing appends to a tool result:
// pre/post context plus the "Hook feedback: …" of a post block. Additions are
// redacted here because the Go-error transcript path does not cross the
// result redaction boundary; the success path redacts the whole result after
// these additions land, so every hook text reaches the transcript masked.
func hookExtraText(preContext string, post *hooks.Aggregated) string {
	var parts []string
	if preContext != "" {
		parts = append(parts, preContext)
	}
	if post != nil {
		if post.Context != "" {
			parts = append(parts, post.Context)
		}
		if post.Decision == hooks.DecisionDeny {
			reason := post.Reason
			if reason == "" {
				reason = "blocked by hook"
			}
			parts = append(parts, "Hook feedback: "+reason)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	extra, _ := tools.RedactToolOutput(strings.Join(parts, "\n"))
	return extra
}

// applyHookOutput applies a post firing's whole-replacement updated_output to
// the result's primary text field (the one Result.String renders).
func applyHookOutput(r tools.Result, post *hooks.Aggregated) tools.Result {
	if post == nil || post.UpdatedOutput == nil {
		return r
	}
	if r.Error != "" {
		r.Error = *post.UpdatedOutput
	} else {
		r.Output = *post.UpdatedOutput
	}
	return r
}

// appendHookExtra appends hook text to the result's primary text field.
func appendHookExtra(r tools.Result, extra string) tools.Result {
	if extra == "" {
		return r
	}
	if r.Error != "" {
		r.Error += "\n" + extra
	} else if r.Output != "" {
		r.Output += "\n" + extra
	} else {
		r.Output = extra
	}
	return r
}

// --- compaction hooks ---

// ErrCompactBlockedByHook reports that a PreCompact hook denied the summary
// request. Callers must NOT fall back to placeholder compaction: the deny
// blocks compaction entirely.
var ErrCompactBlockedByHook = errors.New("compaction blocked by hook")

// compactTriggerKey carries the compaction trigger ("auto" or "manual") to
// the summary request without changing CompactWithSummary's signature.
type compactTriggerKey struct{}

// withCompactTrigger marks a compaction's origin for the PreCompact payload.
func withCompactTrigger(ctx context.Context, trigger string) context.Context {
	return context.WithValue(ctx, compactTriggerKey{}, trigger)
}

// compactTriggerFrom reads the trigger, defaulting to "auto".
func compactTriggerFrom(ctx context.Context) string {
	if v, ok := ctx.Value(compactTriggerKey{}).(string); ok && v != "" {
		return v
	}
	return "auto"
}

// preCompactHookFunc builds the ContextManager.PreCompact callback for a
// runner. A deny blocks the summary request; emit (may be nil) surfaces
// diagnostics. Returns nil when no PreCompact hooks are configured, keeping
// the no-hooks path untouched.
func preCompactHookFunc(r *hooks.Runner, sessionID, cwd string, emit func(LoopEvent)) func(context.Context, string) (bool, string) {
	if !r.Has(hooks.PreCompact) {
		return nil
	}
	return func(ctx context.Context, trigger string) (bool, string) {
		in := hooks.Input{SessionID: sessionID, CWD: cwd, Trigger: trigger}
		agg := r.Run(ctx, hooks.PreCompact, in)
		if emit != nil && len(agg.Diagnostics) > 0 {
			text := fmt.Sprintf("[hooks] %s: %s", hooks.PreCompact, strings.Join(agg.Diagnostics, "; "))
			emit(LoopEvent{Kind: EventNotice, Text: safetext.Sanitize(text)})
		}
		if agg.Decision == hooks.DecisionDeny {
			reason := agg.Reason
			if reason == "" {
				reason = "blocked by hook"
			}
			return true, reason
		}
		return false, ""
	}
}

// postCompactHookFunc builds the ContextManager.PostCompact callback, fired
// after a summary was persisted.
func postCompactHookFunc(r *hooks.Runner, sessionID, cwd string, emit func(LoopEvent)) func(context.Context, string) {
	if !r.Has(hooks.PostCompact) {
		return nil
	}
	return func(ctx context.Context, trigger string) {
		in := hooks.Input{SessionID: sessionID, CWD: cwd, Trigger: trigger}
		agg := r.Run(ctx, hooks.PostCompact, in)
		if emit != nil && len(agg.Diagnostics) > 0 {
			text := fmt.Sprintf("[hooks] %s: %s", hooks.PostCompact, strings.Join(agg.Diagnostics, "; "))
			emit(LoopEvent{Kind: EventNotice, Text: safetext.Sanitize(text)})
		}
	}
}

// installCompactHooks points the ContextManager's hook callbacks at this
// loop's runner. Called at each compaction site so a runner installed after
// the ContextManager was built is still honored.
func (a *AgentLoop) installCompactHooks(cm *ContextManager) {
	if cm == nil {
		return
	}
	sessionID := ""
	if a.Session != nil {
		sessionID = a.Session.ID()
	}
	cm.PreCompact = preCompactHookFunc(a.Hooks, sessionID, a.WorkspaceRoot, a.emit)
	cm.PostCompact = postCompactHookFunc(a.Hooks, sessionID, a.WorkspaceRoot, a.emit)
	// restate the live plan/todo list in the compaction summary prompt.
	cm.PlanSnapshot = func() string {
		if a.Plan != nil {
			return a.Plan.Render()
		}
		return ""
	}
}
