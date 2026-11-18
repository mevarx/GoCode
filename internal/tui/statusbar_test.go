package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/agent"
)

// Mascot has no textual fallback, so it's dropped last; model id absorbs squeeze.
func TestStatusBarAlwaysShowsTheMascot(t *testing.T) {
	// Long HF id that triggered old fallback at ordinary widths.
	const model = "hf.co/dealignai/Ornith-1.5-9B-UNCENSORED-GGUF:Q4_K_M"

	widths := []int{40, 50, 60, 70, 76, 80, 100, 120}
	for _, w := range widths {
		for _, streaming := range []bool{false, true} {
			m := &Model{width: w, providerName: "ollama", modelName: model, streaming: streaming}
			m.mascot = newMascot()
			if streaming {
				m.mascot.setState(mascotWorking, msgNow())
			}

			got := stripANSI(m.renderStatusBar())
			if !strings.Contains(got, "(") || !strings.Contains(got, ")") {
				t.Errorf("width %d streaming=%v: mascot missing from status bar: %q",
					w, streaming, got)
			}
			if strings.Contains(got, "GoCode") {
				t.Errorf("width %d streaming=%v: fell back to the wordmark: %q",
					w, streaming, got)
			}
		}
	}
}

func TestStatusBarFitsItsWidth(t *testing.T) {
	const model = "hf.co/dealignai/Ornith-1.5-9B-UNCENSORED-GGUF:Q4_K_M"
	for _, w := range []int{40, 60, 80, 120} {
		m := &Model{width: w, providerName: "ollama", modelName: model, streaming: true}
		m.mascot = newMascot()
		m.mascot.setState(mascotWorking, msgNow())

		if got := lipgloss.Width(stripANSI(m.renderStatusBar())); got > w {
			t.Errorf("width %d: status bar is %d cells, overflows", w, got)
		}
	}
}

// Narrow bar drops id, not provider or streaming state.
func TestStatusBarKeepsTheStreamingState(t *testing.T) {
	const model = "hf.co/dealignai/Ornith-1.5-9B-UNCENSORED-GGUF:Q4_K_M"
	m := &Model{width: 44, providerName: "ollama", modelName: model, streaming: true}
	m.mascot = newMascot()
	m.mascot.setState(mascotWorking, msgNow())

	got := stripANSI(m.renderStatusBar())
	if !strings.Contains(got, "working") {
		t.Errorf("streaming state missing from a narrow status bar: %q", got)
	}
	if !strings.Contains(got, "ollama") {
		t.Errorf("provider name missing from a narrow status bar: %q", got)
	}
}

// While a turn runs the bar reports elapsed time, the last turn's tokens and
// how full the context window is.
func TestStatusBarShowsTurnMetricsWhileStreaming(t *testing.T) {
	m := &Model{width: 120, providerName: "deepseek", modelName: "qwen3-coder", streaming: true}
	m.mascot = newMascot()
	m.mascot.setState(mascotWorking, msgNow())
	m.turnStarted = time.Now().Add(-3 * time.Second)
	m.lastTurn = agent.TurnMetrics{
		PromptTokens:     1234,
		CompletionTokens: 340,
		ContextTokens:    1540,
		ContextBudget:    10000,
	}

	got := stripANSI(m.renderStatusBar())
	for _, want := range []string{"3s", "↑1.2k", "↓340", "ctx 15%"} {
		if !strings.Contains(got, want) {
			t.Errorf("status bar missing %q: %q", want, got)
		}
	}
	if w := lipgloss.Width(stripANSI(m.renderStatusBar())); w > 120 {
		t.Errorf("status bar is %d cells at width 120", w)
	}
}

// Idle turns show no metric cluster: elbow room goes back to the model id.
func TestStatusBarHidesTurnMetricsWhenIdle(t *testing.T) {
	m := &Model{width: 120, providerName: "deepseek", modelName: "qwen3-coder"}
	m.mascot = newMascot()
	m.turnStarted = time.Now().Add(-3 * time.Second)
	m.lastTurn = agent.TurnMetrics{PromptTokens: 1234, CompletionTokens: 340, ContextTokens: 1540, ContextBudget: 10000}

	if got := stripANSI(m.renderStatusBar()); strings.Contains(got, "ctx ") || strings.Contains(got, "↑") {
		t.Errorf("idle status bar shows turn metrics: %q", got)
	}
}

// Adding metrics must never overflow the bar, whatever the width. The sweep
// includes the boundary where the cluster no longer fits.
func TestStatusBarMetricsNeverOverflow(t *testing.T) {
	for w := 40; w <= 130; w++ {
		m := &Model{width: w, providerName: "deepseek", modelName: "hf.co/x/long-model-name:Q4", streaming: true}
		m.mascot = newMascot()
		m.mascot.setState(mascotWorking, msgNow())
		m.turnStarted = time.Now().Add(-95 * time.Second)
		m.lastTurn = agent.TurnMetrics{PromptTokens: 1234000, CompletionTokens: 98765, ContextTokens: 1332765, ContextBudget: 1500000}

		if got := lipgloss.Width(stripANSI(m.renderStatusBar())); got > w {
			t.Errorf("width %d: status bar is %d cells, overflows", w, got)
		}
	}
}

// A long turn renders minutes without losing the second field.
func TestFormatTurnElapsed(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{9 * time.Second, "9s"},
		{59 * time.Second, "59s"},
		{time.Minute + 4*time.Second, "1m04s"},
		{12*time.Minute + 30*time.Second, "12m30s"},
	}
	for _, tc := range cases {
		if got := formatTurnElapsed(tc.in); got != tc.want {
			t.Errorf("formatTurnElapsed(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
