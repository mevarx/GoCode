package tui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// The scramble spinner primitive. Cycling glyphs are
// drawn from a fixed charset, enter column-by-column on deterministic birth
// steps, and loop through ten pre-rendered frames (500ms at the shared 50ms
// clock). The loop phase is global — floor(now/50ms) — so every spinner in
// the UI agrees about the frame; birth, initialization and the ellipsis cycle
// are per-spinner, measured from its own start.
const (
	// scrambleCharset is the cycling glyph alphabet; every rune is one cell.
	scrambleCharset = "0123456789abcdefABCDEF~!@#$£€%^&*()+=_"
	// scrambleBirthChar holds a column until its birth step.
	scrambleBirthChar = '.'
	// scrambleFrameCount pre-rendered frames ⇒ a 500ms loop at 20fps.
	scrambleFrameCount = 10
	// scrambleDefaultWidth is the primitive's default cycling width (chrome).
	scrambleDefaultWidth = 10
	// barSpinnerWidth is the status bar's compact scramble: chrome, not
	// transcript — it keeps the narrow-width squeeze contract while sharing
	// the clock phase with the full-width rows.
	barSpinnerWidth = 3
	// scrambleTranscriptWidth is the transcript size for tool/assistant cards.
	scrambleTranscriptWidth = 15
	// scrambleBirthWindow bounds every column's birth step: [0, 20) frames,
	// so the whole entrance completes within one second.
	scrambleBirthWindow = 20
	// scrambleInitializedAge is the entrance completion threshold: at
	// framesSinceStart ≥ 20 every column is born and the spinner is settled.
	scrambleInitializedAge = 20
	// ellipsisFramesPerStep advances the ellipsis one frame every 8 clock
	// frames: 8 × 50ms = 400ms per step, 1.6s per full cycle.
	ellipsisFramesPerStep = 8
)

// ellipsisCycle is the label's breathing suffix, suppressed whenever an
// elapsed suffix is rendered instead.
var ellipsisCycle = [...]string{".", "..", "...", ""}

// scrambleBirthStep is column col's deterministic birth frame in [0, 20):
// (7·col) mod 20. Seven is coprime to 20, so adjacent columns spread across
// the whole entrance window instead of clustering, and the same column is
// born at the same frame on every run and every spinner of that width. The
// function is pure — no clock, no randomness, no per-widget state.
func scrambleBirthStep(col int) int {
	return (7 * col) % scrambleBirthWindow
}

// buildScrambleFrames pre-renders the ten-frame loop for a width: frame k,
// column c shows charset[(13·c + 7·k) mod len(charset)]. Both multipliers are
// coprime to the charset length, so neighbours differ and successive frames
// scramble instead of sliding. Pure and reproducible.
func buildScrambleFrames(width int) []string {
	runes := []rune(scrambleCharset)
	frames := make([]string, scrambleFrameCount)
	for k := 0; k < scrambleFrameCount; k++ {
		row := make([]rune, width)
		for c := 0; c < width; c++ {
			row[c] = runes[(13*c+7*k)%len(runes)]
		}
		frames[k] = string(row)
	}
	return frames
}

// The two widths the UI uses are pre-rendered once; any other width (tests,
// future chrome) builds its frames on demand.
var (
	scrambleDefaultFrames    = buildScrambleFrames(scrambleDefaultWidth)
	scrambleTranscriptFrames = buildScrambleFrames(scrambleTranscriptWidth)
)

func scrambleFrames(width int) []string {
	switch width {
	case scrambleDefaultWidth:
		return scrambleDefaultFrames
	case scrambleTranscriptWidth:
		return scrambleTranscriptFrames
	default:
		return buildScrambleFrames(width)
	}
}

// scrambleAge is framesSinceStart: whole 50ms frames since the spinner began,
// with the same boundary slack as the shared clock. A zero start reads as
// already initialized rather than as the zero Time's 292 years.
func scrambleAge(since, now time.Time) int64 {
	if since.IsZero() {
		return scrambleInitializedAge
	}
	d := now.Sub(since) + spinnerTickSlack
	if d < 0 {
		return 0
	}
	return int64(d / clockInterval)
}

// scrambleInitialized reports whether the entrance has completed.
func scrambleInitialized(since, now time.Time) bool {
	return scrambleAge(since, now) >= scrambleInitializedAge
}

// scrambleRow paints the cycling glyph row for one clock sample. The loop
// frame is shared (floor(now/50ms) mod 10); a column shows the birth char
// until its own birth step. Static mode pins frame zero with every column
// born: a functional indicator degrades, it never disappears.
func scrambleRow(width int, since, now time.Time, animated bool) string {
	frames := scrambleFrames(width)
	if !animated {
		return frames[0]
	}
	row := []rune(frames[spinnerFrame(scrambleFrameCount, now, clockInterval)])
	if age := scrambleAge(since, now); age < scrambleInitializedAge {
		for c := 0; c < len(row); c++ {
			if age < int64(scrambleBirthStep(c)) {
				row[c] = scrambleBirthChar
			}
		}
	}
	return string(row)
}

// spinnerEllipsis is the cycling ellipsis for a spinner age. It is suppressed
// whenever an elapsed suffix is shown (the badge replaces it), and pinned to
// the first cycle frame under the motion gate.
func spinnerEllipsis(age int64, hasSuffix, animated bool) string {
	if hasSuffix {
		return ""
	}
	if !animated {
		return ellipsisCycle[0]
	}
	return ellipsisCycle[(age/ellipsisFramesPerStep)%int64(len(ellipsisCycle))]
}

// spinnerGradient blends the theme's accent→highlight→accent ramp across the
// row's runes and scrolls it one cell per clock frame.
// lipgloss.Blend1D interpolates in CIELAB, the perceptual space HCL is the
// cylindrical form of, so the sweep stays even instead of banding in RGB.
func spinnerGradient(steps int, now time.Time) []color.Color {
	if steps <= 0 {
		return nil
	}
	ramp := lipgloss.Blend1D(steps, colorAccent, colorHighlight, colorAccent)
	off := int(clockFrame(now) % int64(steps))
	if off == 0 {
		return ramp
	}
	scrolled := make([]color.Color, steps)
	for i := range ramp {
		scrolled[i] = ramp[(i+off)%steps]
	}
	return scrolled
}

// renderGradientRunes styles every rune with its own colour so the gradient
// never bleeds across a run boundary: the label keeps its own cells even
// where the glyph row's ramp would otherwise spill into it.
func renderGradientRunes(text string, colors []color.Color) string {
	if len(colors) == 0 {
		return text
	}
	var b strings.Builder
	for i, r := range []rune(text) {
		b.WriteString(lipgloss.NewStyle().Foreground(colors[i%len(colors)]).Render(string(r)))
	}
	return b.String()
}

// toolSpinnerHead composes one live card's head from the shared clock: the
// 15-column scramble row, the lifecycle word, and either the cycling ellipsis
// or the elapsed badge — never both. The badge text is whole seconds, so it
// is recomputed every frame but changes at most once a second.
func (m *Model) toolSpinnerHead(status toolCardStatus, since, now time.Time, elapsed time.Duration) string {
	suffix := ""
	if elapsed >= time.Second {
		suffix = formatTurnElapsed(elapsed)
	}
	animated := m.spinnerAnimated()
	glyphs := scrambleRow(scrambleTranscriptWidth, since, now, animated)
	ell := spinnerEllipsis(scrambleAge(since, now), suffix != "", animated)
	text := glyphs + " " + status.stateWord() + ell
	if animated {
		text = renderGradientRunes(text, spinnerGradient(len([]rune(text)), now))
	}
	if suffix != "" {
		text += spinnerSuffixStyle.Render(" · " + suffix)
	}
	return text
}

// barSpinnerGlyphs is the status bar's scramble row: the primitive's default
// width, gradient while motion runs, plain under the gate (the bar's own
// streaming style paints it there).
func (m *Model) barSpinnerGlyphs(now time.Time) string {
	// The status bar is chrome, not transcript: a compact 3-cell scramble
	// keeps the narrow-width squeeze contract (metrics never overflow) while
	// transcript spinners keep the full cycling width.
	row := scrambleRow(barSpinnerWidth, m.turnStarted, now, m.spinnerAnimated())
	if !m.spinnerAnimated() {
		return row
	}
	return renderGradientRunes(row, spinnerGradient(barSpinnerWidth, now))
}
