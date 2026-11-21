package tui

import (
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// The shared spinner clock math. Every live spinner derives its frame from
// the wall clock at render time, so the status bar and every pending tool
// card are frame-locked to each other with zero per-widget state. The single
// generation-guarded tick chain that drives them lives in animclock.go; the
// scramble primitive that reads this phase lives in scramble.go.
const (
	// Below this the animation outruns what a terminal can present and reads
	// as a blur rather than a spinner. It equals the shared clock interval.
	minSpinnerInterval = 50 * time.Millisecond
	// A static spinner still repaints once a second: the elapsed labels are
	// data, not decoration, and they must keep moving under reduced motion.
	staticSpinnerRefresh = time.Second
	// A clock sample within a microsecond of a boundary counts as on it. A
	// floating-point clock reports boundary residue as either zero or a whole
	// period; anchoring upward removes that coin flip (a 1e-6 slack, without
	// which `100 % (1000/30)` reads as 100).
	spinnerTickSlack = time.Microsecond
)

// normalizeSpinnerInterval applies the documented bounds: zero (or negative)
// means static, and anything under the floor is raised to it.
func normalizeSpinnerInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d < minSpinnerInterval {
		return minSpinnerInterval
	}
	return d
}

// spinnerTick is floor(now/interval): the shared phase index every widget
// reads. Zero when static.
func spinnerTick(now time.Time, interval time.Duration) int64 {
	period := normalizeSpinnerInterval(interval)
	if period <= 0 {
		return 0
	}
	return (now.UnixNano() + int64(spinnerTickSlack)) / int64(period)
}

// spinnerFrame is the shared frame index into an n-frame set. Two widgets that
// ask at the same wall-clock instant get the same answer, which is what makes
// them frame-locked without talking to each other.
func spinnerFrame(n int, now time.Time, interval time.Duration) int {
	if n <= 0 {
		return 0
	}
	period := normalizeSpinnerInterval(interval)
	if period <= 0 {
		return 0
	}
	return int(spinnerTick(now, period) % int64(n))
}

// spinnerTickDelay is the delay to the next boundary of the interval, so the
// chain lands on multiples of the period instead of drifting by its own
// execution cost. It is always positive and never longer than one period.
func spinnerTickDelay(now time.Time, interval time.Duration) time.Duration {
	period := normalizeSpinnerInterval(interval)
	if period <= 0 {
		return 0
	}
	next := (spinnerTick(now, period) + 1) * int64(period)
	delay := time.Duration(next - now.UnixNano())
	if delay <= 0 || delay > period {
		// Sitting on a boundary means the frame for it is already on screen;
		// wait for the next one rather than spinning on this one.
		delay = period
	}
	return delay
}

// spinnerAnimated reports whether spinner frames advance. Static mode keeps
// the glyph — a functional indicator degrades, it never disappears.
func (m *Model) spinnerAnimated() bool { return m.decorativeMotion() }

// Shimmer: a band sweeping a label at a fixed velocity, so a long label does
// not sweep more slowly than a short one. Three intensity tiers map onto the
// palette's dedicated shimmer roles, and the renderer opens one style per run
// of same-tier cells rather than per character.
const (
	shimmerCellsPerSecond = 30.0
	// Half of the 12-cell band; outside it the label is at rest.
	shimmerBandHalfWidth = 6
	// Band-free cells before and after the label, so the head enters and
	// leaves off-text instead of popping in at cell zero.
	shimmerPadding = 10
	// Tier cuts on the cosine bump: dim below the first, bright above the
	// second.
	shimmerLowTierCut  = 0.22
	shimmerHighTierCut = 0.65
)

// shimmerPosition is the head's cell position for a label of the given length.
// Velocity is constant, so smoothness never depends on message length.
func shimmerPosition(length int, now time.Time) float64 {
	cycle := float64(length + 2*shimmerPadding)
	if cycle <= 0 {
		return 0
	}
	travel := float64(now.UnixNano()) / float64(time.Second) * shimmerCellsPerSecond
	return math.Mod(travel, cycle)
}

// shimmerTier classifies one cell: 0 dim, 1 mid, 2 bright.
func shimmerTier(cell, length int, now time.Time) int {
	if length <= 0 || cell < 0 || cell >= length {
		return 0
	}
	dist := math.Abs(float64(cell) - shimmerPosition(length, now))
	if dist >= shimmerBandHalfWidth {
		return 0
	}
	intensity := 0.5 * (1 + math.Cos(math.Pi*dist/shimmerBandHalfWidth))
	switch {
	case intensity < shimmerLowTierCut:
		return 0
	case intensity < shimmerHighTierCut:
		return 1
	default:
		return 2
	}
}

// shimmerRun is one stretch of same-tier cells.
type shimmerRun struct {
	tier int
	text string
}

// shimmerRuns groups a label into same-tier runs. One run means one ANSI
// open/close pair, which is the whole reason the band stays cheap.
func shimmerRuns(label string, now time.Time) []shimmerRun {
	runes := []rune(label)
	if len(runes) == 0 {
		return nil
	}
	runs := make([]shimmerRun, 0, len(runes))
	for i, r := range runes {
		tier := shimmerTier(i, len(runes), now)
		if n := len(runs); n > 0 && runs[n-1].tier == tier {
			runs[n-1].text += string(r)
			continue
		}
		runs = append(runs, shimmerRun{tier: tier, text: string(r)})
	}
	return runs
}

// shimmerTierStyles keep the bar's fill and weight but carry no padding: the
// band is painted inside a row that is already padded, and padding per run
// would reflow the bar on every frame. rebuildStyles assigns them so a theme
// switch repaints the shimmer too.
var shimmerTierStyles [3]lipgloss.Style

// renderShimmer paints the band across label for this clock sample.
func renderShimmer(label string, now time.Time) string {
	runs := shimmerRuns(label, now)
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, run := range runs {
		b.WriteString(shimmerTierStyles[run.tier].Render(run.text))
	}
	return b.String()
}
