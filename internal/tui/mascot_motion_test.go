package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A mascot that does not move is not an animation.
//
// The drive oscillator originally ran at 0.22 cycles/frame, which at 20fps is
// 4.4Hz — faster than the 5.2 rad/s bob spring could track. The spring
// attenuated the signal to about 0.06 cells, quantize() rounded that to zero on
// every frame, and the mascot sat perfectly still while the code claimed it was
// animating. Every test still passed: the springs were finite, the faces were
// correct, and nothing crashed.
//
// These assertions measure the rendered motion instead.
func TestBobActuallyMovesEnoughToQuantize(t *testing.T) {
	for _, state := range []mascotState{mascotIdle, mascotThinking, mascotWorking} {
		t.Run(state.String(), func(t *testing.T) {
			m := newMascot()
			m.setState(state, nowForTest())

			moved := 0
			const frames = 600
			var peak float64
			for i := 0; i < frames; i++ {
				m.step()
				if v := m.bob.value(); v > peak {
					peak = v
				}
				if m.cellOffset() != 0 {
					moved++
				}
			}

			// Working must clear a whole cell, or the motion never reaches
			// the screen. Thinking clears half of one, so its cell flips are
			// the visible part of a subtler motion.
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
			// Idle is deliberately sub-cell: a twitchy mascot while the user
			// is reading output is worse than a still one, so only the peak
			// is bounded here, not the cell changes.
			if state != mascotIdle && moved == 0 {
				t.Errorf("%v mascot never changed cell in %d frames", state, frames)
			}
		})
	}
}

// Working must animate more than idle, which is what makes the state readable
// at a glance without reading the status text.
func TestWorkingAnimatesMoreThanIdle(t *testing.T) {
	peak := func(state mascotState) float64 {
		m := newMascot()
		m.setState(state, nowForTest())
		var hi float64
		for i := 0; i < 600; i++ {
			m.step()
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

// The status bar must not reflow while the sprite sways sideways.
func TestInlineWidthSurvivesSway(t *testing.T) {
	m := newMascot()
	m.setState(mascotWorking, nowForTest())

	widths := map[int]bool{}
	for i := 0; i < 600; i++ {
		m.step()
		widths[lipgloss.Width(stripANSI(m.inline(nowForTest(), mascotFaceStyles)))] = true
	}

	if len(widths) != 1 {
		t.Errorf("inline() width varies during the sway: saw %d distinct widths", len(widths))
	}
}
