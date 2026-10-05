package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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
