package tui

import (
	"math"
	"regexp"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/harmonica"
)

// Terminal redraws are expensive and motion above ~20Hz is unreadable, so the
// mascot runs fast while working and slow when the session is quiet.
const (
	idleFPS   = 5
	activeFPS = 20
)

// frameMsg is one animation frame; epoch drops stale ticks from a prior turn.
type frameMsg struct {
	at    time.Time
	epoch int
}

// Indirected so tests can freeze time for deterministic animation.
var msgNow = time.Now

// For slicing rows and comparing styled renders.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

func tickUntil(d time.Duration, epoch int) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return frameMsg{at: t, epoch: epoch} })
}

// Damped oscillator; retargeting mid-flight needs no easing curve.
type spring struct {
	oscillator harmonica.Spring
	pos        float64
	vel        float64
}

// Damping <1 overshoots, =1 settles fastest, >1 crawls.
func newSpring(fps int, frequency, damping float64) spring {
	return spring{oscillator: harmonica.NewSpring(harmonica.FPS(fps), frequency, damping)}
}

func (s *spring) step(target float64) float64 {
	s.pos, s.vel = s.oscillator.Update(s.pos, s.vel, target)
	return s.pos
}

func (s *spring) value() float64 { return s.pos }

// Motion must land on whole cells; clamp so overshoot can't spill upward.
func quantize(v float64, min, max int) int {
	i := int(math.Round(v))
	if i < min {
		return min
	}
	if i > max {
		return max
	}
	return i
}

type blink struct {
	// Zero means not blinking.
	at     time.Time
	every  time.Duration
	closed time.Duration
}

// Rare and short: constant blinking distracts, never blinking looks dead.
func (b blink) isClosed(now time.Time) bool {
	if b.every <= 0 || b.at.IsZero() {
		return false
	}
	elapsed := now.Sub(b.at)
	if elapsed < 0 {
		return false
	}
	if elapsed%b.every >= b.closed {
		return false
	}
	return true
}
