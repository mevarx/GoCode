package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// Springs can stay finite while every frame renders identically, so measure rendered motion.
func TestBobActuallyMovesEnoughToQuantize(t *testing.T) {
	for _, state := range []mascotState{mascotIdle, mascotThinking, mascotWorking} {
		t.Run(state.String(), func(t *testing.T) {
			m := newMascot()
			m.setState(state, nowForTest())

			moved := 0
			const frames = 600
			var peak float64
			for i := 0; i < frames; i++ {
				m.step(msgNow())
				if v := m.bob.value(); v > peak {
					peak = v
				}
				if m.cellOffset() != 0 {
					moved++
				}
			}

			// Working must clear a whole cell; thinking clears half.
			wantPeak := 1.0
			if state == mascotThinking {
				wantPeak = 0.5
			}
			if state == mascotWorking && peak < wantPeak {
				t.Errorf("working bob peaks at %.2f cells, want >= %.2f", peak, wantPeak)
			}
			if state == mascotThinking && peak < wantPeak {
				t.Errorf("thinking bob peaks at %.2f cells, want >= %.2f", peak, wantPeak)
			}
			// Idle is deliberately sub-cell; only peak is bounded here.
			if state != mascotIdle && moved == 0 {
				t.Errorf("%v mascot never changed cell in %d frames", state, frames)
			}
		})
	}
}

// Working must animate more than idle to read at a glance.
func TestWorkingAnimatesMoreThanIdle(t *testing.T) {
	peak := func(state mascotState) float64 {
		m := newMascot()
		m.setState(state, nowForTest())
		var hi float64
		for i := 0; i < 600; i++ {
			m.step(msgNow())
			if v := m.bob.value(); v > hi {
				hi = v
			}
		}
		return hi
	}

	working, idle := peak(mascotWorking), peak(mascotIdle)
	if working <= idle {
		t.Errorf("working peak %.2f must exceed idle peak %.2f", working, idle)
	}
}

// Bar must not reflow while sprite sways sideways.
func TestInlineWidthSurvivesSway(t *testing.T) {
	m := newMascot()
	m.setState(mascotWorking, nowForTest())

	widths := map[int]bool{}
	for i := 0; i < 600; i++ {
		m.step(msgNow())
		widths[lipgloss.Width(stripANSI(m.inline(nowForTest(), mascotFaceStyles)))] = true
	}

	if len(widths) != 1 {
		t.Errorf("inline() width varies during the sway: saw %d distinct widths", len(widths))
	}
}
