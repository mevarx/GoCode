package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// renderStatusBar used to fall back to a plain "GoCode │ provider │ model" line
// whenever the full bar did not fit. That fallback dropped the mascot, so on
// any terminal narrower than 80 columns the mascot never appeared at all — the
// one feature the bar exists to show, removed the moment it was needed most.
//
// The mascot is the only element with no textual fallback, so it is now the
// last thing that may be dropped; the model id absorbs the squeeze instead.
func TestStatusBarAlwaysShowsTheMascot(t *testing.T) {
	// A realistic Hugging Face repo id, which is long enough to have triggered
	// the old fallback at ordinary terminal widths.
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

// The bar must never exceed the terminal width it was sized for.
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

// When the bar cannot fit the model id it drops the id, not the provider or the
// streaming state: an operator needs to know which model is answering.
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
