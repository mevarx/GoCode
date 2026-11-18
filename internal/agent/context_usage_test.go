package agent

import (
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// effectiveHistory builds a history whose local estimate is comfortably under
// a 4000-token budget, with enough turns that trimming is observable.
func effectiveHistory() []provider.Message {
	history := []provider.Message{{Role: "system", Content: "system prompt"}}
	for i := 0; i < 10; i++ {
		history = append(history,
			provider.Message{Role: "user", Content: strings.Repeat("q", 200)},
			provider.Message{Role: "assistant", Content: strings.Repeat("a", 200)},
		)
	}
	return history
}

func TestEffectiveTokensTakesTheLargerValue(t *testing.T) {
	cm := NewContextManager(1000)

	if got := cm.EffectiveTokens(500); got != 500 {
		t.Errorf("EffectiveTokens(500) with no report = %d, want 500", got)
	}

	cm.ObserveProviderUsage(800, 300)
	if got := cm.EffectiveTokens(500); got != 1100 {
		t.Errorf("EffectiveTokens(500) = %d, want the provider's 1100", got)
	}
	if got := cm.EffectiveTokens(2000); got != 2000 {
		t.Errorf("EffectiveTokens(2000) = %d, want the larger local estimate", got)
	}

	// A zero report is not an observation: keeping the previous value beats
	// replacing it with a wrong zero when a provider omits usage.
	cm.ObserveProviderUsage(0, 0)
	if got := cm.EffectiveTokens(500); got != 1100 {
		t.Errorf("a zero report changed the observation: got %d, want 1100", got)
	}
}

// A provider count above the budget must shrink the trimming target even when
// the local estimate alone would have said the window fits.
func TestTruncateShrinksWhenProviderReportsMoreThanBudget(t *testing.T) {
	history := effectiveHistory()

	baseline := NewContextManager(4000)
	before := len(baseline.Truncate(history))
	if before != len(history) {
		t.Fatalf("test premise broken: history was trimmed without a report (%d of %d)", before, len(history))
	}

	cm := NewContextManager(4000)
	cm.ObserveProviderUsage(8000, 1000) // effective 9000 against a 4000 budget
	after := len(cm.Truncate(history))
	if after >= before {
		t.Fatalf("provider-reported context did not tighten trimming: %d messages before, %d after", before, after)
	}
	if after == 0 {
		t.Fatal("trimming removed the whole window")
	}
}

// A provider report smaller than the estimate changes nothing: the estimate is
// the larger value and trimming stays exactly as it was.
func TestTruncateIgnoresSmallerProviderReport(t *testing.T) {
	history := effectiveHistory()

	cm := NewContextManager(4000)
	cm.ObserveProviderUsage(1, 1)
	if got := len(cm.Truncate(history)); got != len(history) {
		t.Errorf("a small provider report trimmed history to %d of %d messages", got, len(history))
	}
}

// /context must show what the model actually has, not just the local guess.
func TestFormatContextStatsUsesEffectiveTokens(t *testing.T) {
	cm := NewContextManager(10000)
	history := []provider.Message{{Role: "user", Content: "hello"}}

	plain := cm.FormatContextStats(history)
	if strings.Contains(plain, "8000") {
		t.Fatalf("test premise broken: estimate already reports 8000 tokens: %q", plain)
	}

	cm.ObserveProviderUsage(7500, 500)
	got := cm.FormatContextStats(history)
	if !strings.Contains(got, "8000 / 10000") {
		t.Errorf("stats do not report the provider count: %q", got)
	}
	if !strings.Contains(got, "80.0%") {
		t.Errorf("stats do not report the effective percentage: %q", got)
	}
}

// Compaction removes the content the report described, so the stale count must
// not keep scaling future trims.
func TestCompactClearsProviderReportedContext(t *testing.T) {
	cm := NewContextManager(4000)
	cm.ObserveProviderUsage(9000, 0)

	compacted := cm.Compact(effectiveHistory(), 1)
	if len(compacted) == 0 {
		t.Fatal("compaction returned an empty history")
	}
	if cm.ProviderReportedContext != 0 {
		t.Errorf("ProviderReportedContext = %d after compaction, want 0", cm.ProviderReportedContext)
	}
}
