package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
)

// Automatic context-management triggers. One pass prunes old tool output and
// summarizes the turns before the keep_recent_tokens tail; a provider overflow
// rejection forces the same pass even when the local estimate disagrees.
//
// Slash-command turns return before streamResponseGuarded, so they never reach
// these paths.

// contextWindow resolves the model window used for threshold math. A nil
// registry or unknown model falls back to the registry's own default.
func (a *AgentLoop) contextWindow() int {
	if a.Registry == nil || a.Session == nil {
		return 0
	}
	return a.Registry.ContextWindow(a.Session.Model())
}

// autoCompactDue reports whether the session is over the compaction threshold.
func (a *AgentLoop) autoCompactDue(history []provider.Message) bool {
	cm := a.ContextManager
	if cm == nil || !cm.AutoCompact {
		return false
	}
	local := cm.estimateHistoryTokens(history)
	effective := cm.EffectiveTokens(local)
	window := a.contextWindow()
	if effective > cm.AutoCompactThreshold(window) {
		return true
	}
	// remaining-context backstop: compact when the space left
	// in the real model window falls to 20k tokens (>200k windows) or 20%
	// (smaller windows). The budget threshold above usually fires first; this
	// guards the edge where the provider's own count approaches the window.
	if window > 0 {
		limit := window / 5
		if window > 200000 {
			limit = 20000
		}
		if window-effective <= limit {
			return true
		}
	}
	return false
}

// autoCompactNow runs at most one automatic pass and persists it. force skips
// the threshold check: a provider overflow proves the window is smaller than
// the local estimate assumed. It reports whether history changed.
func (a *AgentLoop) autoCompactNow(ctx context.Context, force bool) bool {
	cm := a.ContextManager
	if cm == nil || !cm.AutoCompact || a.Session == nil || a.Registry == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}

	history := a.Session.History()
	if !force && !a.autoCompactDue(history) {
		return false
	}

	var p provider.Provider
	if active, err := a.Registry.Active(); err == nil {
		p = active
	}

	// PreCompact/PostCompact wrap the summary request. The trigger rides
	// the context so CompactWithSummary's signature stays unchanged.
	a.installCompactHooks(cm)
	ctx = withCompactTrigger(ctx, "auto")

	outcome := cm.AutoCompactHistory(ctx, p, a.Session.Model(), history)
	if errors.Is(outcome.SummaryErr, ErrCompactBlockedByHook) {
		reason := strings.TrimPrefix(outcome.SummaryErr.Error(), ErrCompactBlockedByHook.Error()+": ")
		a.emit(LoopEvent{Kind: EventNotice, Text: fmt.Sprintf("[Context compaction blocked by hook: %s]", reason)})
	}
	if !outcome.Changed {
		return false
	}

	a.Session.LoadMessages(outcome.History)
	// Rebuild from current workspace/plan/scratchpad state: compaction keeps
	// the system message, but the prompt is per-run and may be stale.
	a.EnsureSystemPrompt()

	// persist the model summary as the replay anchor so a resumed session
	// reads from it onward (pre-anchor rows stay in the database).
	if outcome.Summarized {
		a.Session.RecordSummaryAnchor(outcome.History)
	}

	// PostCompact fires after the summary was persisted.
	if outcome.Summarized && cm.PostCompact != nil {
		cm.PostCompact(ctx, "auto")
	}

	if notice := autoCompactNotice(outcome); notice != "" {
		a.emit(LoopEvent{Kind: EventNotice, Text: notice})
	}
	return true
}

// autoCompactNotice renders the user-facing summary of one automatic pass.
func autoCompactNotice(outcome AutoCompactOutcome) string {
	switch {
	case outcome.DroppedTurns > 0 && outcome.Summarized:
		text := fmt.Sprintf("[Context compacted automatically: %d earlier turns summarized", outcome.DroppedTurns)
		if outcome.PrunedTokens > 0 {
			text += fmt.Sprintf(" (~%d tokens of old tool output elided)", outcome.PrunedTokens)
		}
		return text + ".]"
	case outcome.DroppedTurns > 0:
		text := fmt.Sprintf("[Context compacted automatically: %d earlier turns compacted with a placeholder", outcome.DroppedTurns)
		if outcome.SummaryErr != nil {
			text += fmt.Sprintf(" (provider summary unavailable: %v)", outcome.SummaryErr)
		}
		return text + ".]"
	case outcome.PrunedTokens > 0:
		return fmt.Sprintf("[Context pruned automatically: elided old tool output (~%d tokens).]", outcome.PrunedTokens)
	default:
		return ""
	}
}

// maxOverflowRecoveries bounds overflow handling to one compaction retry per
// round. A second overflow falls through to truncation and is never looped.
const maxOverflowRecoveries = 1

// openStream opens one provider request, recovering from a context-overflow
// rejection by compacting and retrying the same round. Any other failure is
// returned as a stream error. The second overflow falls back to the existing
// truncation path with an explicit notice rather than silently dropping turns.
func (a *AgentLoop) openStream(ctx context.Context, p provider.Provider, model string, specs []provider.ToolSpec, history []provider.Message, m *turnMetrics) (<-chan provider.StreamChunk, error) {
	recoveries := 0
	for {
		ch, err := p.Stream(ctx, model, history, specs)
		if err == nil {
			return ch, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isContextOverflowError(err) {
			return nil, fmt.Errorf("stream error: %w", err)
		}

		if recoveries >= maxOverflowRecoveries {
			// Second overflow: the window is still too small after compaction.
			// Fall through to truncation once, and never loop.
			truncated, dropped := a.ContextManager.TruncateReport(a.Session.History())
			if dropped == 0 {
				a.emit(LoopEvent{Kind: EventNotice, Text: "[Context overflow again after automatic compaction: no older turns remain to drop, so the request was not retried.]"})
				return nil, fmt.Errorf("stream error: %w", err)
			}
			a.emit(LoopEvent{Kind: EventNotice, Text: fmt.Sprintf("[Context overflow again after automatic compaction: dropped %d earlier turns and retried once with truncated history.]", dropped)})
			history = truncated
			m.addPromptTokens(history)
			continue
		}

		recoveries++
		changed := false
		if a.ContextManager != nil && a.ContextManager.AutoCompact {
			a.emit(LoopEvent{Kind: EventNotice, Text: "[Context overflow: compacting history and retrying the request once.]"})
			changed = a.autoCompactNow(ctx, true)
			if !changed {
				a.emit(LoopEvent{Kind: EventNotice, Text: "[Context overflow: no older turns could be compacted; falling back to truncation.]"})
			}
		} else {
			a.emit(LoopEvent{Kind: EventNotice, Text: "[Context overflow: automatic compaction is disabled; falling back to truncation.]"})
		}

		// The retry always passes through the existing truncation path, which
		// also repairs pairing after compaction.
		truncated, dropped := a.ContextManager.TruncateReport(a.Session.History())
		if dropped > 0 {
			changed = true
			a.emit(LoopEvent{Kind: EventNotice, Text: fmt.Sprintf("[Context overflow: dropped %d earlier turns without summarization before the retry.]", dropped)})
		}
		if !changed {
			a.emit(LoopEvent{Kind: EventNotice, Text: "[Context overflow: history is already minimal, so the request was not retried.]"})
			return nil, fmt.Errorf("stream error: %w", err)
		}
		history = truncated
		m.addPromptTokens(history)
	}
}
