package agent

import (
	"log/slog"
	"math"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
)

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

	toolCalls   int
	toolLatency map[string]time.Duration

	autoApproved int
	approved     int
	denied       int

	truncations int

	// slashCommand marks a turn that ran a command instead of a model call,
	// so zero token counts read as expected rather than as a bug.
	slashCommand bool

	// Filled in by log, so a caller never has to know the clock.
	duration    time.Duration
	compactions int
	retries     int64
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

// addPromptTokens records the estimated size of one request's prompt.
func (m *turnMetrics) addPromptTokens(history []provider.Message) {
	if m.cm == nil {
		return
	}
	for _, msg := range history {
		m.promptTokens += m.cm.EstimateTokens(msg)
	}
}

// addCompletion records one streamed response. Reasoning is billed as output
// even though it is never displayed, so it counts toward the estimate.
func (m *turnMetrics) addCompletion(content, reasoning string) {
	m.completionChars += len(content)
	m.completionTokens += estimateTextTokens(content) + estimateTextTokens(reasoning)
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

// log emits the single "turn.metrics" record for this turn. Only names,
// counts, sizes, and timings are included.
func (m *turnMetrics) log() {
	m.duration = time.Since(m.started)
	m.retries = provider.RetryCount.Load() - m.retryBaseline
	if m.cm != nil {
		m.compactions = int(m.cm.Compactions.Load() - m.compactionBaseline)
	}

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
	)
}

func atLeastOne(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
