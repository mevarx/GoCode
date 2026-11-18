package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
)

// Automatic context management. The threshold math follows omp's rule adapted
// to GoCode's resolved budget, and every cut point is a whole-turn boundary, so
// a tool result can never be separated from the call that declared it.
const (
	// DefaultKeepRecentTokens is the verbatim tail automatic compaction keeps
	// when no configured value is set. Manual /compact is unaffected: it stays
	// at two turns.
	DefaultKeepRecentTokens = 20000

	// compactionReserveFraction is the share of the model window reserved for
	// the response (and provider framing) above the compaction threshold.
	compactionReserveFraction = 0.15
	// minCompactionReserve is the smallest reserve, so small windows still
	// leave room for an answer.
	minCompactionReserve = 16384

	// Pruning-lite limits: the newest tool output is protected, short results
	// are never blanked, and a prune must save enough to be worth rewriting
	// the prompt prefix.
	pruneProtectTokens   = 40000
	pruneMinResultTokens = 50
	pruneSavingsFloor    = 8192

	prunedToolOutputFormat = "[output truncated — ~%d tokens]"
)

// keepRecentTokens resolves the configured verbatim tail, applying the default
// for unset or nonsensical values.
func (cm *ContextManager) keepRecentTokens() int {
	if cm == nil || cm.KeepRecentTokens <= 0 {
		return DefaultKeepRecentTokens
	}
	return cm.KeepRecentTokens
}

// AutoCompactThreshold returns the effective-token count above which automatic
// compaction should run for a model window. The budget is the resolved one
// (80% of the window, or the configured override). The reserve is 15% of the
// window, at least minCompactionReserve, and at most half the budget: a small
// window must not produce a negative threshold.
func (cm *ContextManager) AutoCompactThreshold(window int) int {
	if cm == nil {
		return 0
	}
	budget := cm.MaxTokens
	if budget <= 0 {
		budget = 8192
	}
	if window <= 0 {
		window = 65536
	}

	reserve := int(float64(window) * compactionReserveFraction)
	if reserve < minCompactionReserve {
		reserve = minCompactionReserve
	}
	if half := budget / 2; reserve > half {
		reserve = half
	}
	if reserve < 0 {
		reserve = 0
	}
	return budget - reserve
}

// validCutPoint reports whether cutting before turns[cut] leaves a window that
// starts at a user message. Because a turn is never split, such a cut cannot
// strand a tool result whose call was summarized away.
func validCutPoint(turns []Turn, cut int) bool {
	if cut <= 0 || cut >= len(turns) {
		return false
	}
	msgs := turns[cut].Messages
	return len(msgs) > 0 && msgs[0].Role == "user"
}

// autoCompactCutPoint chooses where automatic compaction starts keeping
// history: the earliest user-message boundary whose retained tail still fits
// keep_recent_tokens. It always retains at least the newest turn, returns 0
// when no valid cut exists (a single turn, or a window starting mid-turn that
// has no user boundary to cut at), and therefore never separates a call from
// its result.
func (cm *ContextManager) autoCompactCutPoint(turns []Turn) int {
	if len(turns) < 2 {
		return 0
	}

	keep := cm.keepRecentTokens()
	acc := turns[len(turns)-1].Tokens(cm)
	cut := len(turns) - 1
	for i := len(turns) - 2; i >= 1; i-- {
		tokens := turns[i].Tokens(cm)
		if acc+tokens > keep {
			break
		}
		acc += tokens
		cut = i
	}

	if !validCutPoint(turns, cut) {
		return 0
	}
	return cut
}

// PruneToolResults replaces oversized, old tool output with a token-count
// placeholder. The newest pruneProtectTokens of tool output are protected,
// results smaller than pruneMinResultTokens are never touched, and nothing is
// rewritten unless the total saving reaches pruneSavingsFloor. Only the content
// of tool messages changes, so call/result pairing stays valid.
//
// Plan and scratchpad state live in the system prompt, not history, so pruning
// cannot lose them.
func PruneToolResults(history []provider.Message) ([]provider.Message, int) {
	if len(history) == 0 {
		return history, 0
	}

	protected := make([]bool, len(history))
	acc := 0
	for i := len(history) - 1; i >= 0 && acc < pruneProtectTokens; i-- {
		if history[i].Role != "tool" {
			continue
		}
		acc += estimateTextTokens(history[i].Content)
		protected[i] = true
	}

	type replacement struct {
		index   int
		content string
	}
	var replacements []replacement
	saved := 0
	for i, msg := range history {
		if msg.Role != "tool" || protected[i] {
			continue
		}
		tokens := estimateTextTokens(msg.Content)
		if tokens < pruneMinResultTokens {
			continue
		}
		placeholder := fmt.Sprintf(prunedToolOutputFormat, tokens)
		gain := tokens - estimateTextTokens(placeholder)
		if gain <= 0 {
			continue
		}
		saved += gain
		replacements = append(replacements, replacement{index: i, content: placeholder})
	}

	if saved < pruneSavingsFloor || len(replacements) == 0 {
		return history, 0
	}

	out := make([]provider.Message, len(history))
	copy(out, history)
	for _, r := range replacements {
		out[r.index].Content = r.content
	}
	return out, saved
}

// AutoCompactOutcome reports what one automatic context-management pass did.
type AutoCompactOutcome struct {
	// History is the rewritten history; when Changed is false it is the input.
	History []provider.Message
	// Changed reports whether anything (pruning or compaction) was applied.
	Changed bool
	// Summarized is true when the dropped span was replaced by a model-written
	// summary rather than the count-only placeholder.
	Summarized bool
	// PrunedTokens is the estimated token saving from pruning old tool output.
	PrunedTokens int
	// DroppedTurns counts the whole turns folded into the summary.
	DroppedTurns int
	// SummaryErr is set when the summary request failed and the placeholder
	// fallback was used instead.
	SummaryErr error
}

// AutoCompactHistory prunes old tool output and, when a pair-safe cut point
// exists, replaces the turns older than the keep_recent_tokens tail with a
// summary. A summary failure falls back to the existing count-only placeholder.
// It never returns an error: when nothing can be done safely it reports
// Changed=false and leaves history alone.
func (cm *ContextManager) AutoCompactHistory(ctx context.Context, p provider.Provider, model string, history []provider.Message) AutoCompactOutcome {
	outcome := AutoCompactOutcome{History: history}
	if cm == nil || len(history) == 0 {
		return outcome
	}

	pruned, saved := PruneToolResults(history)
	if saved > 0 {
		outcome.History = pruned
		outcome.PrunedTokens = saved
		// The provider's last report described the pre-prune window; keeping
		// it would make the next request look as large as before.
		cm.ProviderReportedContext = 0
	}

	_, nonSystem := splitSystemMessages(outcome.History)
	turns := groupIntoTurns(nonSystem)
	cut := cm.autoCompactCutPoint(turns)
	if cut <= 0 {
		outcome.Changed = outcome.PrunedTokens > 0
		return outcome
	}

	// cut counts the omitted turns, so the retained tail is len(turns)-cut
	// whole turns. CompactWithSummary keeps exactly that many.
	keepTurns := len(turns) - cut
	var compacted []provider.Message
	err := fmt.Errorf("compact: no provider available for summary")
	if p != nil {
		compacted, err = cm.CompactWithSummary(ctx, p, model, outcome.History, keepTurns)
	}
	if err == nil {
		outcome.Summarized = true
	} else {
		outcome.SummaryErr = err
		compacted = cm.Compact(outcome.History, keepTurns)
	}

	outcome.History = compacted
	outcome.Changed = true
	outcome.DroppedTurns = cut
	return outcome
}

// isContextOverflowError reports whether err is a provider rejection of a
// request that was too large for the model's context window. Errors arrive
// normalized to text by the provider layer, so this classifies the wire
// message each dialect sends for that condition.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	for _, marker := range []string{
		"context_length_exceeded",           // OpenAI and OpenAI-compatible gateways
		"maximum context length",            // OpenAI prose
		"context length exceeded",           // generic gateways
		"prompt is too long",                // Anthropic
		"input is too long",                 // Anthropic-shaped proxies
		"exceeds the context length",        // Ollama
		"exceeds the context window",        // generic
		"context window exceeded",           // generic
		"reduce the length of the messages", // some Azure deployments
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}

	// Generic 400: some gateways return only a status plus prose. Require both
	// an overflow word and a context/token subject so unrelated 400s (bad
	// model, bad parameter) are not treated as overflow.
	if strings.Contains(msg, "400") || strings.Contains(msg, "invalid_request_error") {
		overflow := strings.Contains(msg, "exceed") ||
			strings.Contains(msg, "too long") ||
			strings.Contains(msg, "too many") ||
			strings.Contains(msg, "overflow")
		subject := strings.Contains(msg, "context") || strings.Contains(msg, "token")
		return overflow && subject
	}

	return false
}
