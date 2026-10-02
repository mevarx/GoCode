package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// mascotState drives the face, the spring constants and the motion cadence.
type mascotState int

const (
	// mascotIdle is the resting state between turns.
	mascotIdle mascotState = iota
	// mascotThinking covers the gap between a prompt being sent and the first
	// token arriving — the model is working but has said nothing yet.
	mascotThinking
	// mascotWorking is an active stream.
	mascotWorking
	// mascotSuccess is a turn that finished cleanly.
	mascotSuccess
	// mascotError is a turn that failed.
	mascotError
)

// String is the state name shown in the status line.
func (s mascotState) String() string {
	switch s {
	case mascotThinking:
		return "thinking"
	case mascotWorking:
		return "working"
	case mascotSuccess:
		return "done"
	case mascotError:
		return "error"
	default:
		return "idle"
	}
}

// face is the mascot's expression: two eyes and a mouth.
type face struct {
	left, right, mouth string
}

// faces holds the expression for each state. Every glyph is single-cell so the
// silhouette never changes width between states; a status bar that reflows
// mid-animation is unreadable.
var faces = map[mascotState]face{
	mascotIdle:     {left: "•", right: "•", mouth: "︶"},
	mascotThinking: {left: "◐", right: "◑", mouth: "︵"},
	mascotWorking:  {left: "●", right: "●", mouth: "︶"},
	mascotSuccess:  {left: "^", right: "^", mouth: "︵"},
	mascotError:    {left: "×", right: "×", mouth: "︷"},
}

// mascot is GoCode's character, drawn from box-drawing runes. Spring-driven
// rather than frame-indexed, so it decelerates instead of snapping between poses.
type mascot struct {
	state mascotState

	// bob is the vertical offset in cells. Under-damped so it overshoots
	// slightly, which is what makes a hop read as a hop.
	bob spring
	// sway is the horizontal offset, gentler than bob.
	sway spring

	// phase advances continuously; oscillating it is what the springs chase.
	phase float64

	// amp is the current motion amplitude in cells, eased toward the value
	// amplitude() reports for the current state so a state change eases in
	// rather than snapping.
	amp float64

	eyes blink
}

// newMascot builds the mascot at rest.
func newMascot() mascot {
	return mascot{
		state: mascotIdle,
		// Damping 0.55 is under-damped: it overshoots once and settles, which
		// reads as weight. At 1.0 the motion would feel like a machine.
		bob: newSpring(activeFPS, 5.2, 0.55),
		// Horizontal motion is slower and tighter; too much sway reads as
		// drifting rather than alive.
		sway: newSpring(activeFPS, 3.1, 0.7),
		eyes: blink{every: 4 * time.Second, closed: 110 * time.Millisecond},
	}
}

// cadence is the frame interval for a state. Working states animate fast
// enough to feel responsive; idle is slow so a parked session costs almost
// nothing.
func cadence(s mascotState) time.Duration {
	switch s {
	case mascotWorking:
		return time.Second / activeFPS
	case mascotThinking:
		return time.Second / (activeFPS / 2)
	default:
		return time.Second / idleFPS
	}
}

// amplitude is how far the mascot moves, in cells, per state. Idle uses a
// sub-cell amplitude that rounds to nothing most frames — a mascot frozen at
// rest is calmer than one always twitching.
func amplitude(s mascotState) float64 {
	switch s {
	case mascotWorking:
		return 1.6
	case mascotThinking:
		return 0.9
	default:
		return 0.45
	}
}

// setState moves the mascot to a new state, resetting the blink so a change of
// expression reads as a reaction rather than a random blink.
func (m *mascot) setState(s mascotState, now time.Time) {
	if m.state == s {
		return
	}
	m.state = s
	m.eyes.at = now
}

// rest returns the mascot at its neutral pose.
//
// The banner draws a static portrait, so it must not inherit a mid-flight
// spring: hero() trims rows when the bob is positive, and a portrait that
// randomly loses its antenna looks broken.
func (m mascot) rest() mascot {
	m.bob.pos, m.bob.vel = 0, 0
	m.sway.pos, m.sway.vel = 0, 0
	m.amp = 0
	m.phase = 0.25
	return m
}

// step advances the animation by one frame.
func (m *mascot) step() {
	// Arm the blink on the first frame: setState cannot do it because it
	// no-ops when the state is unchanged.
	if m.eyes.at.IsZero() {
		m.eyes.at = msgNow()
	}

	// Ease the amplitude toward the target for the current state so switching
	// states ramps the motion instead of snapping.
	m.amp += (amplitude(m.state) - m.amp) * amplitudeEaseRate

	// The oscillator runs continuously; the springs chase it. Using a phase
	// counter rather than a frame index keeps motion smooth when the frame
	// rate changes between states.
	m.phase += m.rate()
	if m.phase >= 1 {
		m.phase -= 1
	}

	wave := waveAt(m.phase)
	m.bob.step(m.amp * wave)
	m.sway.step(m.amp * 0.5 * -wave)
}

// amplitudeEaseRate is the per-frame fraction by which motion amplitude
// approaches its target. Lower is gentler.
const amplitudeEaseRate = 0.12

// rate is the oscillator speed in cycles per frame. Above 0.03 the bob spring
// stops tracking, quantize() rounds the result to zero, and the mascot freezes
// while still reporting itself as animating.
func (m *mascot) rate() float64 {
	return 0.03
}

// waveAt maps a 0..1 phase onto a -1..1 triangle: constant slope, sharp sign
// flip at the corners, no dwell at the extremes. The spring does the smoothing.
func waveAt(phase float64) float64 {
	t := phase * 2
	var v float64
	if t < 1 {
		v = t
	} else {
		v = 2 - t
	}
	return v*2 - 1
}

// cellOffset is the mascot's vertical offset in whole terminal cells.
func (m *mascot) cellOffset() int {
	return quantize(m.bob.value(), -1, 1)
}

// faceFor returns the current expression, accounting for blinks.
func (m *mascot) faceFor(now time.Time) face {
	f, ok := faces[m.state]
	if !ok {
		f = faces[mascotIdle]
	}
	if m.state == mascotIdle || m.state == mascotThinking {
		if m.eyes.isClosed(now) {
			// A flat line, deliberately unlike the curved mouth glyphs: when
			// eyes and mouth rendered the same shape the mascot looked like it
			// was grimacing rather than blinking.
			f.left, f.right = "─", "─"
		}
	}
	return f
}

// inline renders the mascot as a single line, for the status bar.
//
// Width is fixed at 7 cells in every state so the status bar never reflows:
// six glyphs, but the mouth (︶︵︷) is presentation-form and measures two cells.
func (m mascot) inline(now time.Time, st mascotStyles) string {
	f := m.faceFor(now)

	// The trailing pad reserves the sway spring's sideways shift, keeping the
	// field width provably constant. Normally empty: sway peaks below a cell.
	const cellWidth = 7
	// spriteCells is the unpadded width: "(", two eyes, a space, ")" and the
	// mouth, whose glyph is two cells wide.
	const spriteCells = 7
	shift := quantize(m.sway.value(), 0, 1)
	pad := strings.Repeat(" ", cellWidth-spriteCells+shift)

	var b strings.Builder
	b.WriteString(st.body.Render("("))
	b.WriteString(st.eye.Render(f.left + " " + f.right))
	b.WriteString(st.body.Render(")"))
	b.WriteString(st.mouth.Render(f.mouth))
	b.WriteString(st.body.Render(pad))
	return b.String()
}

// hero renders the mascot as a multi-line figure for the startup banner. The
// antenna tip uses the same bob value as the inline sprite, so the two read as
// one creature rather than two drawings.
func (m mascot) hero(now time.Time, st mascotStyles) string {
	f := m.faceFor(now)
	tip := "●"

	// faceWidth is the interior width of the mascot's face. Both the eye row
	// and the mouth row must fill it exactly or the right border drifts.
	const faceWidth = 5

	// total is the width of the face box, including its two-space indent. The
	// antenna is padded to the same width so the portrait is a clean rectangle
	// rather than a narrow stalk floating over a wider box.
	const total = 2 + faceWidth + 2

	rows := []string{
		st.tip.Render(padTo(tip, total)),
		st.body.Render(padTo("│", total)),
		// The ┴ is where the antenna meets the lid; without it the stalk
		// reads as passing through the box rather than into it.
		st.body.Render("  ╭──┴──╮"),
		st.body.Render("  │") + st.eye.Render(padTo(f.left+" "+f.right, faceWidth)) + st.body.Render("│"),
		st.body.Render("  │") + st.mouth.Render(padTo(f.mouth, faceWidth)) + st.body.Render("│"),
		st.body.Render("  ╰" + strings.Repeat("─", faceWidth) + "╯"),
	}

	// A rising bob adds a blank row above so the whole figure moves up; a
	// falling one adds one below, keeping the antenna joint intact either way.
	offset := m.cellOffset()
	blank := padTo("", total)
	for ; offset > 0; offset-- {
		rows = append([]string{blank}, rows...)
	}
	for ; offset < 0; offset++ {
		rows = append(rows, blank)
	}
	return strings.Join(rows, "\n")
}

// padTo centres s within width cells. Input wider than width is returned
// unchanged rather than truncated: the only caller passes single glyphs into a
// five-cell interior, so silently dropping characters would be worse.
func padTo(s string, width int) string {
	n := lipgloss.Width(s)
	if n >= width {
		return s
	}
	left := (width - n) / 2
	right := width - n - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}
