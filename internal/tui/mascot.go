package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

type mascotState int

const (
	mascotIdle mascotState = iota
	// Gap between prompt sent and first token arriving.
	mascotThinking
	mascotWorking
	mascotSuccess
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

type face struct {
	left, right, mouth string
}

// Every glyph is single-cell so the bar never reflows mid-animation.
var faces = map[mascotState]face{
	mascotIdle:     {left: "•", right: "•", mouth: "︶"},
	mascotThinking: {left: "◐", right: "◑", mouth: "︵"},
	mascotWorking:  {left: "●", right: "●", mouth: "︶"},
	mascotSuccess:  {left: "^", right: "^", mouth: "︵"},
	mascotError:    {left: "×", right: "×", mouth: "︷"},
}

// Spring-driven so it decelerates instead of snapping between poses.
type mascot struct {
	state mascotState

	// Vertical offset; under-damped so overshoot reads as a hop.
	bob  spring
	sway spring

	// Phase advances continuously; springs chase it.
	phase float64

	// Eased toward amplitude() so state changes ramp instead of snapping.
	amp float64

	eyes blink
}

func newMascot() mascot {
	return mascot{
		state: mascotIdle,
		// 0.55 overshoots once for weight; 1.0 would feel mechanical.
		bob: newSpring(activeFPS, 5.2, 0.55),
		// Slower and tighter; too much sway reads as drifting.
		sway: newSpring(activeFPS, 3.1, 0.7),
		eyes: blink{every: 4 * time.Second, closed: 110 * time.Millisecond},
	}
}

// Working is fast to feel responsive; idle is slow so parked sessions cost nothing.
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

// Idle is sub-cell so rest stays calm instead of twitching.
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

// Resets blink so the new expression reads as a reaction.
func (m *mascot) setState(s mascotState, now time.Time) {
	if m.state == s {
		return
	}
	m.state = s
	m.eyes.at = now
}

// Banner needs a neutral pose; mid-flight bob would trim the antenna.
func (m mascot) rest() mascot {
	m.bob.pos, m.bob.vel = 0, 0
	m.sway.pos, m.sway.vel = 0, 0
	m.amp = 0
	m.phase = 0.25
	return m
}

// Uses the frame timestamp so delayed frames advance by schedule time, not processing time.
func (m *mascot) step(now time.Time) {
	// setState no-ops when unchanged, so arm blink on first frame.
	if m.eyes.at.IsZero() {
		m.eyes.at = now
	}

	// Ramp amplitude so state switches don't snap.
	m.amp += (amplitude(m.state) - m.amp) * amplitudeEaseRate

	// Phase counter keeps motion smooth across frame-rate changes.
	m.phase += m.rate()
	if m.phase >= 1 {
		m.phase -= 1
	}

	wave := waveAt(m.phase)
	m.bob.step(m.amp * wave)
	m.sway.step(m.amp * -wave)
}

const amplitudeEaseRate = 0.12

// Above 0.03 the spring stops tracking and the mascot freezes while reporting motion.
func (m *mascot) rate() float64 {
	return 0.03
}

// 0..1 phase to -1..1 triangle; spring does the smoothing.
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

func (m *mascot) cellOffset() int {
	return quantize(m.bob.value(), -1, 1)
}

func (m *mascot) faceFor(now time.Time) face {
	f, ok := faces[m.state]
	if !ok {
		f = faces[mascotIdle]
	}
	if m.state == mascotIdle || m.state == mascotThinking {
		if m.eyes.isClosed(now) {
			// Flat line unlike mouth glyphs; matching shapes looked like grimacing.
			f.left, f.right = "─", "─"
		}
	}
	return f
}

// 7-cell sprite in an 8-cell field; spare cell is sway travel so the bar never reflows.
func (m mascot) inline(now time.Time, st mascotStyles) string {
	f := m.faceFor(now)

	const spriteCells = 7
	const cellWidth = spriteCells + 1
	shift := quantize(m.sway.value(), 0, cellWidth-spriteCells)

	var b strings.Builder
	b.WriteString(st.body.Render(strings.Repeat(" ", shift)))
	b.WriteString(st.body.Render("("))
	b.WriteString(st.eye.Render(f.left + " " + f.right))
	b.WriteString(st.body.Render(")"))
	b.WriteString(st.mouth.Render(f.mouth))
	b.WriteString(st.body.Render(strings.Repeat(" ", cellWidth-spriteCells-shift)))
	return b.String()
}

// Antenna shares inline's bob so both read as one creature.
func (m mascot) hero(now time.Time, st mascotStyles) string {
	f := m.faceFor(now)
	tip := "●"

	// Eye and mouth rows must fill it exactly or the right border drifts.
	const faceWidth = 5

	// Antenna padded to same width so portrait stays a clean rectangle.
	const total = 2 + faceWidth + 2

	// Stalk sits above the ┴ joint; padTo would centre incl. indent and land one cell left.
	antennaCol := 2 + 1 + (faceWidth-1)/2
	antennaRow := func(glyph string) string {
		return strings.Repeat(" ", antennaCol) + glyph +
			strings.Repeat(" ", total-antennaCol-lipgloss.Width(glyph))
	}

	rows := []string{
		st.tip.Render(antennaRow(tip)),
		st.body.Render(antennaRow("│")),
		// ┴ joint keeps the stalk from reading as passing through the box.
		st.body.Render("  ╭──┴──╮"),
		st.body.Render("  │") + st.eye.Render(padTo(f.left+" "+f.right, faceWidth)) + st.body.Render("│"),
		st.body.Render("  │") + st.mouth.Render(padTo(f.mouth, faceWidth)) + st.body.Render("│"),
		st.body.Render("  ╰" + strings.Repeat("─", faceWidth) + "╯"),
	}

	// Rising bob pads above, falling pads below, keeping the joint intact.
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

// Oversized input returns unchanged rather than truncated.
func padTo(s string, width int) string {
	n := lipgloss.Width(s)
	if n >= width {
		return s
	}
	left := (width - n) / 2
	right := width - n - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}
