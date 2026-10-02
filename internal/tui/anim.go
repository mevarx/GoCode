package tui

import (
	"math"
	"regexp"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
)

// Terminal redraws are expensive and motion above ~20Hz is unreadable, so the
// mascot runs fast while working and slow when the session is quiet.
const (
	idleFPS   = 5
	activeFPS = 20
)

// frameMsg advances every animation in the UI by one frame.
//
// epoch identifies the turn the frame belongs to, so a tick still in flight at
// a turn boundary cannot re-arm under the next turn and compound the chain.
type frameMsg struct {
	at    time.Time
	epoch int
}

// msgNow reads the clock. Indirected through a variable so tests can freeze
// time and make blink and animation behaviour deterministic.
var msgNow = time.Now

// stripANSI removes escape sequences. lipgloss.Width already measures styled
// input correctly; this is for slicing rows and comparing two styled renders.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// tickUntil returns a command that emits frameMsg after d, tagged with the turn
// it belongs to.
func tickUntil(d time.Duration, epoch int) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return frameMsg{at: t, epoch: epoch} })
}

// spring is a damped harmonic oscillator driving one animated property. It
// accelerates and decelerates on its own, so retargeting mid-flight needs no
// easing curve to tune.
type spring struct {
	oscillator harmonica.Spring
	pos        float64
	vel        float64
}

// newSpring builds a spring at the given frame rate.
//
// Damping below 1 overshoots, 1 arrives fastest without wobble, above 1 crawls.
func newSpring(fps int, frequency, damping float64) spring {
	return spring{oscillator: harmonica.NewSpring(harmonica.FPS(fps), frequency, damping)}
}

// step advances the spring one frame toward target and returns the new value.
func (s *spring) step(target float64) float64 {
	s.pos, s.vel = s.oscillator.Update(s.pos, s.vel, target)
	return s.pos
}

// value returns the current position without advancing.
func (s *spring) value() float64 { return s.pos }

// quantize maps a float onto an integer cell range, rounding to nearest.
//
// Motion must land on whole cells or it looks stepped and breaks alignment.
// Clamping rather than wrapping stops an overshooting spring spilling upward.
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

// blink describes an eye-openness cycle in frames.
type blink struct {
	// at is when the blink started; zero means not blinking.
	at    time.Time
	every time.Duration
	// closed is how long the eyes stay shut.
	closed time.Duration
}

// isClosed reports whether the eyes should be drawn shut at now.
//
// Rare and short on purpose: constant blinking distracts, never blinking
// looks dead.
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
