package tui

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// These tests protect the two things the mascot animation can silently break:
// the physical layout (inline() must occupy the same number of cells in every
// state, blink phase and colour scheme, and a bob that overshoots must never
// push cellOffset outside -1..1) and the simulation contract (springs converge
// and genuinely wobble, blinks are rare and short, and each state's
// cadence/amplitude ordering is what makes "busy" read as busy). A layout
// regression is invisible in a unit test until the status bar tears, so every
// width below is measured from real rendered output rather than assumed.

var animANSISeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

// animVisibleWidth measures the printable cells a styled string occupies.
func animVisibleWidth(s string) int {
	return lipgloss.Width(animANSISeq.ReplaceAllString(s, ""))
}

// animAllStates is the closed set of states the UI can put the mascot in.
func animAllStates() []mascotState {
	return []mascotState{mascotIdle, mascotThinking, mascotWorking, mascotSuccess, mascotError}
}

// --- spring ---------------------------------------------------------------

func TestSpringConvergesOnTarget(t *testing.T) {
	cases := []struct {
		name      string
		damping   float64
		frequency float64
		target    float64
	}{
		{name: "mascot bob constants", damping: 0.55, frequency: 5.2, target: 1},
		{name: "mascot sway constants", damping: 0.7, frequency: 3.1, target: -1},
		{name: "critically damped", damping: 1, frequency: 5.2, target: 1},
		{name: "overdamped", damping: 1.8, frequency: 4, target: 1},
		{name: "negative target", damping: 0.55, frequency: 5.2, target: -1.6},
		{name: "target at rest", damping: 0.55, frequency: 5.2, target: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSpring(activeFPS, tc.frequency, tc.damping)
			for i := 0; i < 400; i++ {
				s.step(tc.target)
			}
			if math.Abs(s.value()-tc.target) > 0.01 {
				t.Errorf("after 400 frames spring is at %v, want %v (within 0.01)", s.value(), tc.target)
			}
			// Position alone is not convergence: a spring passing through
			// the target also matches, so velocity has to have died too.
			if math.Abs(s.vel) > 0.01 {
				t.Errorf("spring converged in position but is still moving: vel=%v", s.vel)
			}
		})
	}
}

// The whole reason for harmonica over a linear ramp: damping below 1 must
// actually carry the value past the target. If this stops happening the mascot
// stops reading as alive.
func TestSpringOvershootsWhenUnderDamped(t *testing.T) {
	cases := []struct {
		name      string
		damping   float64
		frequency float64
		wantOver  bool
	}{
		{name: "bob overshoots", damping: 0.55, frequency: 5.2, wantOver: true},
		{name: "sway overshoots", damping: 0.7, frequency: 3.1, wantOver: true},
		{name: "lightly damped overshoots", damping: 0.3, frequency: 4, wantOver: true},
		{name: "critically damped does not", damping: 1, frequency: 5.2, wantOver: false},
		{name: "overdamped does not", damping: 1.6, frequency: 4, wantOver: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const target = 1.0
			s := newSpring(activeFPS, tc.frequency, tc.damping)
			peak := 0.0
			for i := 0; i < 400; i++ {
				if v := s.step(target); v > peak {
					peak = v
				}
			}
			overshot := peak > target
			if overshot != tc.wantOver {
				if tc.wantOver {
					t.Errorf("expected an overshoot past %v, peak was %v", target, peak)
				} else {
					t.Errorf("expected no overshoot past %v, peak was %v", target, peak)
				}
			}
		})
	}
}

// --- quantize -------------------------------------------------------------

func TestQuantizeRoundsAndClamps(t *testing.T) {
	cases := []struct {
		name string
		v    float64
		min  int
		max  int
		want int
	}{
		{name: "exact integer", v: 2, min: -100, max: 100, want: 2},
		{name: "rounds half away from zero, positive", v: 0.5, min: -100, max: 100, want: 1},
		{name: "rounds half away from zero, negative", v: -0.5, min: -100, max: 100, want: -1},
		{name: "just below half rounds down", v: 0.49, min: -100, max: 100, want: 0},
		{name: "just above half rounds up", v: 0.51, min: -100, max: 100, want: 1},
		{name: "below half, negative", v: -0.49, min: -100, max: 100, want: 0},
		{name: "zero", v: 0, min: -1, max: 1, want: 0},
		{name: "negative integer in range", v: -3, min: -5, max: 5, want: -3},
		{name: "clamps below min", v: -9.4, min: -1, max: 1, want: -1},
		{name: "clamps above max", v: 9.4, min: -1, max: 1, want: 1},
		{name: "rounds up into max", v: 0.9, min: -1, max: 1, want: 1},
		{name: "rounds down into min", v: -0.9, min: -1, max: 1, want: -1},
		{name: "rounds before clamping, not the other way", v: -1.4, min: -1, max: 1, want: -1},
		{name: "huge positive overshoot clamps to max", v: 1e9, min: -1, max: 1, want: 1},
		{name: "huge negative overshoot clamps to min", v: -1e9, min: -1, max: 1, want: -1},
		{name: "zero-width range", v: 5, min: 0, max: 0, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quantize(tc.v, tc.min, tc.max); got != tc.want {
				t.Errorf("quantize(%v, %d, %d) = %d, want %d", tc.v, tc.min, tc.max, got, tc.want)
			}
		})
	}
}

func TestQuantizeNeverEscapesItsRange(t *testing.T) {
	const lo, hi = -1, 1
	v := -20.0
	for i := 0; i <= 4000; i++ {
		got := quantize(v, lo, hi)
		if got < lo || got > hi {
			t.Fatalf("quantize(%v, %d, %d) = %d, outside range", v, lo, hi, got)
		}
		v += 0.01
	}
}

// --- blink ----------------------------------------------------------------

func TestBlinkIsClosedWindow(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	const every = 4 * time.Second
	const closed = 110 * time.Millisecond

	cases := []struct {
		name string
		b    blink
		now  time.Time
		want bool
	}{
		{name: "closed at the start of a cycle", b: blink{at: start, every: every, closed: closed}, now: start, want: true},
		{name: "closed mid-window", b: blink{at: start, every: every, closed: closed}, now: start.Add(50 * time.Millisecond), want: true},
		{name: "closed just before the window ends", b: blink{at: start, every: every, closed: closed}, now: start.Add(closed - time.Nanosecond), want: true},
		{name: "open at the window boundary", b: blink{at: start, every: every, closed: closed}, now: start.Add(closed), want: false},
		{name: "open between blinks", b: blink{at: start, every: every, closed: closed}, now: start.Add(2 * time.Second), want: false},
		{name: "open late in the cycle", b: blink{at: start, every: every, closed: closed}, now: start.Add(every - time.Millisecond), want: false},
		{name: "closed again on the second cycle", b: blink{at: start, every: every, closed: closed}, now: start.Add(every + 50*time.Millisecond), want: true},
		{name: "disabled when every is zero", b: blink{at: start, every: 0, closed: closed}, now: start, want: false},
		{name: "disabled when every is negative", b: blink{at: start, every: -time.Second, closed: closed}, now: start, want: false},
		{name: "disabled before the first blink", b: blink{every: every, closed: closed}, now: start, want: false},
		{name: "zero value blink never closes", b: blink{}, now: start, want: false},
		{name: "open when now precedes the blink start", b: blink{at: start, every: every, closed: closed}, now: start.Add(-time.Second), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.isClosed(tc.now); got != tc.want {
				t.Errorf("isClosed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBlinkIsRareAndShort(t *testing.T) {
	// A blink that never reopens is a mascot with the eyes glued shut; one that
	// fires constantly is just noise. Both ends are regressions.
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	b := newMascot().eyes
	if b.every <= 0 || b.closed <= 0 {
		t.Fatalf("mascot blink is disabled: %+v", b)
	}
	if b.closed >= b.every {
		t.Errorf("closed window %v is not shorter than the %v cycle", b.closed, b.every)
	}

	// A blink with no start time never fires, so arm it explicitly.
	b.at = start
	closedSamples := 0
	const total = 4000
	for i := 0; i < total; i++ {
		if b.isClosed(start.Add(time.Duration(i) * b.every / 100)) {
			closedSamples++
		}
	}
	if closedSamples == 0 {
		t.Error("blink never closed during the sampled window")
	}
	// Sampling at every/100 resolution, closed is roughly closed/every of the time.
	if closedSamples > total/4 {
		t.Errorf("eyes were closed for %d of %d samples; blink is too frequent", closedSamples, total)
	}
}

// --- mascot state ---------------------------------------------------------

func TestMascotStateString(t *testing.T) {
	cases := []struct {
		state mascotState
		want  string
	}{
		{state: mascotIdle, want: "idle"},
		{state: mascotThinking, want: "thinking"},
		{state: mascotWorking, want: "working"},
		{state: mascotSuccess, want: "done"},
		{state: mascotError, want: "error"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.state.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("unknown states fall back to idle", func(t *testing.T) {
		for _, s := range []mascotState{-7, mascotState(99)} {
			if got := s.String(); got != "idle" {
				t.Errorf("mascotState(%d).String() = %q, want %q", int(s), got, "idle")
			}
		}
	})

	t.Run("names are unique", func(t *testing.T) {
		seen := map[string]bool{}
		for _, s := range animAllStates() {
			if seen[s.String()] {
				t.Errorf("duplicate state name %q", s.String())
			}
			seen[s.String()] = true
		}
	})
}

func TestMascotFacesCoverEveryState(t *testing.T) {
	for _, s := range animAllStates() {
		t.Run(s.String(), func(t *testing.T) {
			f, ok := faces[s]
			if !ok {
				t.Fatalf("no face defined for state %v", s)
			}
			for part, glyph := range map[string]string{"left": f.left, "right": f.right, "mouth": f.mouth} {
				if strings.TrimSpace(glyph) == "" {
					t.Errorf("%s glyph is empty", part)
				}
			}
			// The thinking face deliberately uses mismatched eyes (◐ vs ◑), so
			// the invariant is equal cell width rather than equal glyphs.
			if lipgloss.Width(f.left) != lipgloss.Width(f.right) {
				t.Errorf("eyes differ in width: %q is %d cells, %q is %d",
					f.left, lipgloss.Width(f.left), f.right, lipgloss.Width(f.right))
			}
		})
	}
}

func TestMascotCadenceOrdering(t *testing.T) {
	for _, s := range animAllStates() {
		t.Run(s.String(), func(t *testing.T) {
			if d := cadence(s); d <= 0 {
				t.Errorf("cadence(%v) = %v, want a positive frame interval", s, d)
			}
		})
	}

	// A smaller interval means a faster animation.
	if cadence(mascotWorking) >= cadence(mascotIdle) {
		t.Errorf("working cadence %v must be faster than idle cadence %v",
			cadence(mascotWorking), cadence(mascotIdle))
	}
	if cadence(mascotThinking) >= cadence(mascotIdle) {
		t.Errorf("thinking cadence %v must be faster than idle cadence %v",
			cadence(mascotThinking), cadence(mascotIdle))
	}
	if cadence(mascotWorking) >= cadence(mascotThinking) {
		t.Errorf("working cadence %v must be faster than thinking cadence %v",
			cadence(mascotWorking), cadence(mascotThinking))
	}
	if cadence(mascotSuccess) != cadence(mascotIdle) || cadence(mascotError) != cadence(mascotIdle) {
		t.Errorf("terminal states should fall back to the idle cadence, got success=%v error=%v idle=%v",
			cadence(mascotSuccess), cadence(mascotError), cadence(mascotIdle))
	}
	if got, want := cadence(mascotWorking), time.Second/activeFPS; got != want {
		t.Errorf("working cadence = %v, want %v (one frame at activeFPS)", got, want)
	}
}

func TestMascotAmplitudeOrdering(t *testing.T) {
	working, thinking, idle := amplitude(mascotWorking), amplitude(mascotThinking), amplitude(mascotIdle)
	if !(working > thinking && thinking > idle) {
		t.Errorf("expected working(%v) > thinking(%v) > idle(%v)", working, thinking, idle)
	}
	if idle <= 0 {
		t.Errorf("idle amplitude %v must be positive", idle)
	}
	if thinking <= 0.5 {
		t.Errorf("thinking amplitude %v rounds to no motion most frames", thinking)
	}
	for _, s := range animAllStates() {
		if a := amplitude(s); a < 0 {
			t.Errorf("amplitude(%v) = %v, must not be negative", s, a)
		}
	}
}

func TestMascotRateOrdering(t *testing.T) {
	m := newMascot()
	rates := map[mascotState]float64{}
	for _, s := range animAllStates() {
		m.state = s
		rates[s] = m.rate()
		if rates[s] <= 0 || rates[s] >= 1 {
			t.Errorf("rate() for %v = %v, want a fraction of a cycle per frame", s, rates[s])
		}
	}
	if !(rates[mascotWorking] > rates[mascotThinking] && rates[mascotThinking] > rates[mascotIdle]) {
		t.Errorf("expected working(%v) > thinking(%v) > idle(%v)",
			rates[mascotWorking], rates[mascotThinking], rates[mascotIdle])
	}
}

// --- wave -----------------------------------------------------------------

func TestWaveAtStaysInRange(t *testing.T) {
	for i := 0; i <= 1000; i++ {
		phase := float64(i) / 1000
		v := waveAt(phase)
		if math.IsNaN(v) {
			t.Fatalf("waveAt(%v) = NaN", phase)
		}
		if v < -1 || v > 1 {
			t.Fatalf("waveAt(%v) = %v, outside [-1, 1]", phase, v)
		}
	}
}

// Convention in the source: the wave starts at its LOW extreme (-1) at phase 0
// and peaks at +1 at phase 0.5, returning to -1 at phase 1.
func TestWaveAtExtremes(t *testing.T) {
	cases := []struct {
		name  string
		phase float64
		want  float64
	}{
		{name: "trough at phase zero", phase: 0, want: -1},
		{name: "rises through the first quarter", phase: 0.25, want: 0},
		{name: "peak at half phase", phase: 0.5, want: 1},
		{name: "falls through the third quarter", phase: 0.75, want: 0},
		{name: "back to trough at full phase", phase: 1, want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := waveAt(tc.phase); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("waveAt(%v) = %v, want %v", tc.phase, got, tc.want)
			}
		})
	}
}

func TestWaveAtIsContinuousAcrossCycleBoundary(t *testing.T) {
	// step() wraps phase back into [0,1); a discontinuity at the wrap would
	// show up as a single-frame jerk in the mascot.
	prev := waveAt(0)
	for i := 1; i <= 2000; i++ {
		phase := float64(i%1000) / 1000
		cur := waveAt(phase)
		if d := math.Abs(cur - prev); d > 0.01 {
			t.Fatalf("waveAt jumped by %v at phase %v (prev %v, cur %v)", d, phase, prev, cur)
		}
		prev = cur
	}
}

// --- mascot behaviour ------------------------------------------------------

func TestMascotFaceForNeverEmpty(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, s := range animAllStates() {
		t.Run(s.String(), func(t *testing.T) {
			m := newMascot()
			m.state = s
			f := m.faceFor(now)
			if f.left == "" || f.right == "" || f.mouth == "" {
				t.Errorf("faceFor(%v) has an empty glyph: %+v", s, f)
			}
		})
	}
}

func TestMascotUnknownStateFallsBackToIdleFace(t *testing.T) {
	m := newMascot()
	m.state = mascotState(42)
	if got, want := m.faceFor(time.Now()), faces[mascotIdle]; got != want {
		t.Errorf("faceFor for an unknown state = %+v, want the idle face %+v", got, want)
	}
}

func TestMascotStateTransitionsDoNotPanic(t *testing.T) {
	states := animAllStates()
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	// Every ordered pair, then a long run of frames through each, exercising
	// setState/step/faceFor/inline/hero/cellOffset together.
	for _, from := range states {
		for _, to := range states {
			m := newMascot()
			m.setState(from, base)
			m.setState(to, base.Add(100*time.Millisecond))
			if m.state != to {
				t.Errorf("setState(%v) left the mascot in %v", to, m.state)
			}
			// setState is a documented no-op when the state is unchanged, so
			// only a real transition is expected to move `since`.
			if from != to && !m.since.Equal(base.Add(100*time.Millisecond)) {
				t.Errorf("%v→%v did not record the transition time, since=%v", from, to, m.since)
			}
			for i := 0; i < 120; i++ {
				now := base.Add(time.Duration(i) * cadence(to))
				m.step()
				if off := m.cellOffset(); off < -1 || off > 1 {
					t.Fatalf("%v→%v frame %d: cellOffset %d outside [-1, 1]", from, to, i, off)
				}
				_ = m.faceFor(now)
				_ = m.inline(now, mascotFaceStyles)
				_ = m.hero(now, mascotFaceStyles)
			}
		}
	}
}

func TestMascotRepeatedSetStateIsANoOp(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newMascot()
	m.setState(mascotWorking, base)
	m.setState(mascotWorking, base.Add(time.Second))
	if !m.since.Equal(base) {
		t.Errorf("re-entering the same state reset the transition time: since=%v, want %v", m.since, base)
	}
}

func TestMascotStepStaysFinite(t *testing.T) {
	m := newMascot()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for i, s := range animAllStates() {
		m.setState(s, now.Add(time.Duration(i)*time.Second))
		for f := 0; f < 200; f++ {
			m.step()
			if math.IsNaN(m.bob.value()) || math.IsInf(m.bob.value(), 0) {
				t.Fatalf("bob became %v in state %v after %d frames", m.bob.value(), s, f)
			}
			if math.IsNaN(m.sway.value()) || math.IsInf(m.sway.value(), 0) {
				t.Fatalf("sway became %v in state %v after %d frames", m.sway.value(), s, f)
			}
			if m.phase < 0 || m.phase >= 1 {
				t.Fatalf("phase %v escaped [0,1) in state %v", m.phase, s)
			}
			if m.amp < 0 {
				t.Fatalf("amplitude %v went negative in state %v", m.amp, s)
			}
		}
	}
}

func TestMascotAmplitudeEasesRatherThanSnaps(t *testing.T) {
	m := newMascot()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m.setState(mascotWorking, now)
	m.step()
	if math.Abs(m.amp-amplitude(mascotWorking)) < 1e-9 {
		t.Error("amplitude jumped straight to the working target on the first frame; it should ease")
	}
	for i := 0; i < 400; i++ {
		m.step()
	}
	if math.Abs(m.amp-amplitude(mascotWorking)) > 0.01 {
		t.Errorf("amplitude settled at %v, want %v", m.amp, amplitude(mascotWorking))
	}
}

func TestMascotBlinkClosesIdleEyes(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newMascot()
	// setState is a no-op while already idle, so leave idle first: a mascot
	// that never transitions has no blink start time and never blinks at all.
	m.setState(mascotWorking, start)
	m.setState(mascotIdle, start)

	if f := m.faceFor(start.Add(2 * time.Second)); f.left != faces[mascotIdle].left {
		t.Errorf("eyes are %q two seconds into the cycle, want open %q", f.left, faces[mascotIdle].left)
	}
	closed := m.faceFor(start)
	if closed.left == faces[mascotIdle].left {
		t.Error("eyes did not close at the start of a blink")
	}
	if closed.left != closed.right {
		t.Errorf("blink closed the eyes unevenly: %q vs %q", closed.left, closed.right)
	}
}

// Working states are meant to look focused, not sleepy.
func TestMascotDoesNotBlinkWhileWorking(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newMascot()
	m.setState(mascotWorking, start)
	want := faces[mascotWorking]
	for i := 0; i < 200; i++ {
		if f := m.faceFor(start.Add(time.Duration(i) * 50 * time.Millisecond)); f != want {
			t.Fatalf("frame %d changed the working face to %+v, want %+v", i, f, want)
		}
	}
}

// --- layout ---------------------------------------------------------------

// animInlineWidth is the visible cell width of inline(). The render is "(", the
// left eye, a space, the right eye, ")" and the mouth: six glyphs, but the mouth
// is a presentation-form paren that both terminals and lipgloss measure as two
// cells, so the rendered width is 7. Pinned as a constant so a future glyph swap
// has to update this test deliberately rather than silently reflow the bar.
const animInlineWidth = 7

// The status bar must not reflow mid-animation: inline() occupies a fixed cell
// width in every state, at every blink phase, under every colour scheme.
func TestMascotInlineWidthIsConstant(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	styles := []struct {
		name string
		st   mascotStyles
	}{
		{name: "default dark palette", st: mascotFaceStyles},
		{name: "unstyled", st: mascotStyles{}},
		{name: "bold everywhere", st: mascotStyles{
			body:  lipgloss.NewStyle().Bold(true),
			eye:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff0000")),
			mouth: lipgloss.NewStyle().Underline(true),
			tip:   lipgloss.NewStyle().Bold(true),
		}},
	}

	for _, sk := range styles {
		for _, s := range animAllStates() {
			t.Run(sk.name+"/"+s.String(), func(t *testing.T) {
				m := newMascot()
				m.state = s
				// Arm the blink: setState is a no-op for idle, so a mascot that
				// never leaves idle would otherwise never close its eyes.
				m.eyes.at = start
				// Offsets inside, on the edge of, and outside the closed window,
				// plus one well clear of any blink.
				for _, offset := range []time.Duration{0, 10 * time.Millisecond, 109 * time.Millisecond, 110 * time.Millisecond, 3 * time.Second} {
					for i := 0; i < 30; i++ {
						now := start.Add(offset + time.Duration(i)*cadence(s))
						m.step()
						out := m.inline(now, sk.st)
						if got := animVisibleWidth(out); got != animInlineWidth {
							t.Fatalf("inline() width = %d cells in state %v at %v (blink offset %v), want %d: %q",
								got, s, now, offset, animInlineWidth, animANSISeq.ReplaceAllString(out, ""))
						}
					}
				}
			})
		}
	}
}

func TestMascotInlineWidthIsIdenticalAcrossStates(t *testing.T) {
	// The strongest form of the invariant: whatever the absolute width turns out
	// to be, it must be the same for every state and every blink phase.
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	seen := map[int][]mascotState{}
	for _, s := range animAllStates() {
		for _, offset := range []time.Duration{0, 50 * time.Millisecond, 1 * time.Second, 3 * time.Second} {
			m := newMascot()
			m.state = s
			m.eyes.at = start
			m.step()
			w := animVisibleWidth(m.inline(start.Add(offset), mascotFaceStyles))
			seen[w] = append(seen[w], s)
		}
	}
	if len(seen) != 1 {
		t.Errorf("inline() width varies across states: %v", seen)
	}
}

func TestMascotInlineMatchesCurrentFace(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, s := range animAllStates() {
		t.Run(s.String(), func(t *testing.T) {
			m := newMascot()
			m.state = s
			plain := animANSISeq.ReplaceAllString(m.inline(start, mascotStyles{}), "")
			f := m.faceFor(start)
			for _, glyph := range []string{f.left, f.right, f.mouth} {
				if !strings.Contains(plain, glyph) {
					t.Errorf("inline() = %q, missing glyph %q", plain, glyph)
				}
			}
			if strings.ContainsRune(plain, '\n') {
				t.Errorf("inline() must be a single line, got %q", plain)
			}
		})
	}
}

func TestPadToCentresWithinWidth(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "already the right width", in: "abcde", width: 5, want: "abcde"},
		// Odd remainders split evenly: one cell of padding on each side.
		{name: "centred with an odd remainder", in: "abc", width: 5, want: " abc "},
		{name: "even remainder splits evenly", in: "ab", width: 6, want: "  ab  "},
		{name: "empty string", in: "", width: 5, want: "     "},
		// The mouth is a two-cell presentation form, so padTo has to measure by
		// cells rather than runes or the row comes out short.
		{name: "wide glyph measured by cell", in: "︶", width: 5, want: " ︶  "},
		{name: "eyes", in: "● ●", width: 5, want: " ● ● "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := padTo(tc.in, tc.width)
			if got != tc.want {
				t.Errorf("padTo(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
			if w := lipgloss.Width(got); w != tc.width {
				t.Errorf("padTo(%q, %d) is %d cells wide, want %d", tc.in, tc.width, w, tc.width)
			}
		})
	}

	t.Run("oversized input is returned unchanged", func(t *testing.T) {
		// The helper's comment says a too-long value is truncated; the code
		// returns it verbatim instead. Harmless for the only caller (hero
		// always passes a 5-cell face interior) but the comment overstates it.
		if got := padTo("abcdefgh", 5); got != "abcdefgh" {
			t.Errorf("padTo(%q, 5) = %q, want it unchanged", "abcdefgh", got)
		}
	})

	t.Run("output is never narrower than the requested width", func(t *testing.T) {
		for _, s := range []string{"", "•", "︶", "● ●", "x", "︵"} {
			for w := 1; w <= 8; w++ {
				if got := lipgloss.Width(padTo(s, w)); got < w {
					t.Errorf("padTo(%q, %d) is %d cells wide, want at least %d", s, w, got, w)
				}
			}
		}
	})
}

// hero() pads the eye and mouth rows to the same interior width so the right
// border lines up; the antenna rows above the head are narrower by design.
func TestMascotHeroRowsAlign(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newMascot()
	m.setState(mascotWorking, start)

	for i := 0; i < 120; i++ {
		now := start.Add(time.Duration(i) * cadence(mascotWorking))
		m.step()
		rows := strings.Split(m.hero(now, mascotFaceStyles), "\n")
		if len(rows) < 5 {
			t.Fatalf("frame %d: hero has %d rows, expected at least 5", i, len(rows))
		}

		// Every row of the body is the same width, which is what keeps the
		// right-hand border from drifting between frames.
		body := rows[len(rows)-4:]
		want := animVisibleWidth(body[0])
		if want < 9 {
			t.Fatalf("frame %d: hero body is only %d cells wide, expected at least 9", i, want)
		}
		for r, row := range body {
			if got := animVisibleWidth(row); got != want {
				t.Errorf("frame %d body row %d is %d cells wide, want %d: %q",
					i, r, got, want, animANSISeq.ReplaceAllString(row, ""))
			}
		}
	}
}

func TestStripANSIRemovesEscapeSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "abc", want: "abc"},
		{name: "sgr colour", in: "\x1b[31mabc\x1b[0m", want: "abc"},
		{name: "bold true colour", in: "\x1b[1;38;2;255;0;0mabc", want: "abc"},
		{name: "multiple runs", in: "\x1b[31ma\x1b[0mb\x1b[32mc\x1b[0m", want: "abc"},
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripANSI(tc.in); got != tc.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- frames ---------------------------------------------------------------

func TestTickUntilEmitsFrameMsg(t *testing.T) {
	cmd := tickUntil(time.Millisecond, 3)
	if cmd == nil {
		t.Fatal("tickUntil returned a nil command")
	}
	msg := cmd()
	fm, ok := msg.(frameMsg)
	if !ok {
		t.Fatalf("tickUntil produced %T, want frameMsg", msg)
	}
	if fm.epoch != 3 {
		t.Errorf("frameMsg.epoch = %d, want 3: a frame that loses its turn tag can "+
			"re-arm a finished turn's ticker", fm.epoch)
	}
	if fm.at.IsZero() {
		t.Error("frameMsg carries a zero timestamp")
	}
}

func TestFrameMsgCarriesItsTimestamp(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if got := (frameMsg{at: at}).at; !got.Equal(at) {
		t.Errorf("frameMsg.at = %v, want %v", got, at)
	}
}
