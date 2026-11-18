package agent

import (
	"log/slog"
	"math"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
)

// TurnMetrics is the observable summary of one turn, emitted with
// EventTurnEnd. Like the log record it carries counts and timings only: no
// message content, tool arguments, command text or file paths.
type TurnMetrics struct {
	// PromptTokens and CompletionTokens are the provider's own counts when
	// every request in the turn reported usage, and local estimates otherwise.
	PromptTokens     int
	CompletionTokens int
	ToolCalls        int
	Elapsed          time.Duration
	// Retries counts provider retries during the turn.
	Retries int64
	// ProviderReported says which source the token counts came from.
	ProviderReported bool
	// ContextTokens is the effective context size after the last request:
	// max(local estimate, provider-reported prompt+completion) so provider-side
	// compression cannot hide growth. ContextBudget is the resolved budget.
	ContextTokens int
	ContextBudget int
}

// ContextPercent returns how full the context window is, and false when no
// budget was resolved or nothing has been measured yet.
func (tm TurnMetrics) ContextPercent() (float64, bool) {
	if tm.ContextBudget <= 0 || tm.ContextTokens <= 0 {
		return 0, false
	}
	return float64(tm.ContextTokens) / float64(tm.ContextBudget) * 100, true
}

// turnMetrics accumulates one structured observability record per agent turn.
// It deliberately carries no message content, tool arguments, command text, or
// file paths: names and counts are enough to explain a slow or expensive turn
// without leaking data into logs.
type turnMetrics struct {
	provider string
	model    string
	started  time.Time
	cm       *ContextManager

	// Baselines make the counters global to the process but reported per turn.
	retryBaseline      int64
	compactionBaseline int64

	promptTokens     int
	completionChars  int
	completionTokens int

	// Provider-reported usage, summed over the turn, plus the latest request's
	// counts (the context accounting compares against that one) and how many
	// requests reported at all.
	providerPromptTokens     int
	providerCompletionTokens int
	lastReported             provider.Usage
	requests                 int
	reportedRounds           int

	// Local estimates for the latest request, kept apart from the per-turn
	// sums so context accounting compares like with like.
	lastPromptEstimate     int
	lastCompletionEstimate int

	toolCalls   int
	toolLatency map[string]time.Duration

	autoApproved int
	approved     int
	denied       int

	truncations int

	// truncationNoticed suppresses repeat last-resort truncation notices
	// within one turn: every round re-truncates the same unchanged session.
	truncationNoticed bool

	// slashCommand marks a turn that ran a command instead of a model call,
	// so zero token counts read as expected rather than as a bug.
	slashCommand bool

	// Filled in by finalize, so a caller never has to know the clock.
	duration    time.Duration
	compactions int
	retries     int64
	finalized   bool
}

func newTurnMetrics(a *AgentLoop) *turnMetrics {
	m := &turnMetrics{
		started:     time.Now(),
		toolLatency: make(map[string]time.Duration),
		cm:          a.ContextManager,
	}
	if a.Registry != nil {
		m.provider = a.Registry.ActiveName()
	}
	if a.Session != nil {
		m.model = a.Session.Model()
	}
	m.retryBaseline = provider.RetryCount.Load()
	if m.cm != nil {
		m.compactionBaseline = m.cm.Compactions.Load()
	}
	return m
}

// addPromptTokens records the estimated size of one request's prompt and keeps
// that per-request figure for context accounting.
func (m *turnMetrics) addPromptTokens(history []provider.Message) {
	if m.cm == nil {
		return
	}
	total := 0
	for _, msg := range history {
		total += m.cm.EstimateTokens(msg)
	}
	m.promptTokens += total
	m.lastPromptEstimate = total
	m.requests++
}

// addUsage records one request's provider-reported token counts. The latest
// report also feeds ContextManager, which compares it with its own estimate.
func (m *turnMetrics) addUsage(u provider.Usage) {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return
	}
	m.providerPromptTokens += u.PromptTokens
	m.providerCompletionTokens += u.CompletionTokens
	m.lastReported = u
	m.reportedRounds++
	if m.cm != nil {
		m.cm.ObserveProviderUsage(u.PromptTokens, u.CompletionTokens)
	}
}

// addCompletion records one streamed response. Reasoning is billed as output
// even though it is never displayed, so it counts toward the estimate.
func (m *turnMetrics) addCompletion(content, reasoning string) {
	chars := len(content)
	tokens := estimateTextTokens(content) + estimateTextTokens(reasoning)
	m.completionChars += chars
	m.completionTokens += tokens
	m.lastCompletionEstimate = tokens
}

func (m *turnMetrics) beginTool() time.Time {
	m.toolCalls++
	return time.Now()
}

// recordDecision counts an approval outcome. auto means the gate decided
// without asking the user (permission allow/deny, or a tool that needs none).
func (m *turnMetrics) recordDecision(_ string, approved, auto bool) {
	switch {
	case !approved:
		m.denied++
	case auto:
		m.autoApproved++
	default:
		m.approved++
	}
}

func (m *turnMetrics) toolLatencyMillis() map[string]float64 {
	out := make(map[string]float64, len(m.toolLatency))
	for name, d := range m.toolLatency {
		ms := float64(d) / float64(time.Millisecond)
		out[name] = math.Round(ms*1000) / 1000
	}
	return out
}

// finalize freezes the clock-derived fields. Both log and snapshot call it, so
// neither depends on the other having run first.
func (m *turnMetrics) finalize() {
	if m.finalized {
		return
	}
	m.finalized = true
	m.duration = time.Since(m.started)
	m.retries = provider.RetryCount.Load() - m.retryBaseline
	if m.cm != nil {
		m.compactions = int(m.cm.Compactions.Load() - m.compactionBaseline)
	}
}

// providerReported reports whether every provider request in the turn reported
// usage; only then do its counts cover the whole turn.
func (m *turnMetrics) providerReported() bool {
	return m.requests > 0 && m.reportedRounds == m.requests
}

// turnTokens returns the token counts a front-end should show: the provider's
// own numbers when every request reported them, local estimates otherwise.
func (m *turnMetrics) turnTokens() (prompt, completion int) {
	if m.providerReported() {
		return m.providerPromptTokens, m.providerCompletionTokens
	}
	return m.promptTokens, m.completionTokens
}

// contextTokens is the effective context size after the latest request: the
// larger of the local estimate and the provider's own report.
func (m *turnMetrics) contextTokens() int {
	local := m.lastPromptEstimate + m.lastCompletionEstimate
	if m.cm != nil {
		return m.cm.EffectiveTokens(local)
	}
	if reported := m.lastReported.PromptTokens + m.lastReported.CompletionTokens; reported > local {
		return reported
	}
	return local
}

// snapshot returns the public summary emitted with EventTurnEnd. Unlike the log
// record it does not floor counts at one: a command turn genuinely has no
// tokens, and a front-end must be able to tell that from a measured turn.
func (m *turnMetrics) snapshot() *TurnMetrics {
	m.finalize()
	prompt, completion := m.turnTokens()
	tm := &TurnMetrics{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		ToolCalls:        m.toolCalls,
		Elapsed:          m.duration,
		Retries:          m.retries,
		ProviderReported: m.providerReported(),
		ContextTokens:    m.contextTokens(),
	}
	if m.cm != nil {
		tm.ContextBudget = m.cm.MaxTokens
	}
	return tm
}

// log emits the single "turn.metrics" record for this turn. Only names,
// counts, sizes, and timings are included.
func (m *turnMetrics) log() {
	m.finalize()

	promptTokens := m.promptTokens
	completionTokens := m.completionTokens
	if !m.slashCommand {
		// A model turn always has a prompt and an answer, so a zero here
		// would mean the estimate failed rather than that the turn was empty.
		promptTokens = atLeastOne(promptTokens)
		completionTokens = atLeastOne(completionTokens)
	}

	slog.Info("turn.metrics",
		"provider", m.provider,
		"model", m.model,
		"prompt_tokens_est", promptTokens,
		"completion_tokens_est", completionTokens,
		"completion_chars", m.completionChars,
		"duration_ms", float64(m.duration)/float64(time.Millisecond),
		"duration_ns", m.duration.Nanoseconds(),
		"slash_command", m.slashCommand,
		"tool_call_count", m.toolCalls,
		"tool_latency_ms", m.toolLatencyMillis(),
		"approvals_auto", m.autoApproved,
		"approvals_approved", m.approved,
		"approvals_denied", m.denied,
		"truncation", m.truncations > 0,
		"truncations", m.truncations,
		"compactions", m.compactions,
		"provider_retries", m.retries,
		"provider_prompt_tokens", m.providerPromptTokens,
		"provider_completion_tokens", m.providerCompletionTokens,
		"provider_usage_reported", m.providerReported(),
		"context_tokens", m.contextTokens(),
		"context_budget", m.contextBudget(),
	)
}

// contextBudget is the resolved budget, or zero when no manager is attached.
func (m *turnMetrics) contextBudget() int {
	if m.cm == nil {
		return 0
	}
	return m.cm.MaxTokens
}

func atLeastOne(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
