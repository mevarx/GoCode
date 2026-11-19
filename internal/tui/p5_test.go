package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// forceMotionAll pins the capability probe so a test never depends on the
// host's REDUCED_MOTION environment.
func forceMotionAll(t *testing.T) {
	t.Helper()
	old := capabilities.ReducedMotion
	capabilities.ReducedMotion = false
	t.Cleanup(func() { capabilities.ReducedMotion = old })
}

// isBrailleFrame reports whether s is one of the shared spinner glyphs.
func isBrailleFrame(s string) bool {
	for _, f := range brailleSpinnerFrames {
		if s == f {
			return true
		}
	}
	return false
}

// revealAll drives the coalescing tick until nothing is left to reveal.
func revealAll(t *testing.T, m Model) Model {
	t.Helper()
	for i := 0; i < 200; i++ {
		if !m.revealPending() {
			return m
		}
		m = drive(m, transcriptFlushMsg{})
	}
	t.Fatal("the reveal never drained; paced text would never finish")
	return m
}

// --- shared spinner clock -------------------------------------------------

func TestSpinnerTickIsFloorOfTheWallClock(t *testing.T) {
	const period = 80 * time.Millisecond
	base := time.Unix(1_700_000_000, 0)

	cases := []struct {
		name string
		at   time.Duration
		want int64
	}{
		{"at the first boundary", 0, spinnerTick(base, period)},
		{"inside the second period", 79 * time.Millisecond, spinnerTick(base, period)},
		{"exactly one period later", 80 * time.Millisecond, spinnerTick(base, period) + 1},
		{"three periods later", 240 * time.Millisecond, spinnerTick(base, period) + 3},
	}
	first := spinnerTick(base, period)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if tc.at == 0 {
				want = first
			}
			if got := spinnerTick(base.Add(tc.at), period); got != want {
				t.Errorf("spinnerTick(+%v) = %d, want %d", tc.at, got, want)
			}
		})
	}

	// 79 ms and 80 ms must not land in the same tick differently: the boundary
	// is the only place the index moves.
	if spinnerTick(base.Add(79*time.Millisecond), period) != spinnerTick(base, period) {
		t.Error("79 ms into a period advanced the tick; frames would be uneven")
	}
	if spinnerTick(base.Add(80*time.Millisecond), period) == spinnerTick(base, period) {
		t.Error("80 ms into a period did not advance the tick")
	}
}

// Two widgets asking the same clock the same question must get the same frame:
// that is the whole point of deriving phase from the wall clock.
func TestSpinnerFrameIsSharedByEveryWidget(t *testing.T) {
	const period = 80 * time.Millisecond
	base := time.Unix(1_700_000_000, 0)

	for _, offset := range []time.Duration{0, 1 * time.Millisecond, 40 * time.Millisecond, 81 * time.Millisecond, 3 * time.Second} {
		now := base.Add(offset)
		statusBar := spinnerFrame(len(brailleSpinnerFrames), now, period)
		toolCard := spinnerFrame(len(brailleSpinnerFrames), now, period)
		if statusBar != toolCard {
			t.Fatalf("at %v the status bar shows frame %d and the card frame %d", offset, statusBar, toolCard)
		}
		if statusBar < 0 || statusBar >= len(brailleSpinnerFrames) {
			t.Fatalf("frame %d is outside the %d-glyph set", statusBar, len(brailleSpinnerFrames))
		}
	}

	// Successive periods must walk the whole set rather than sticking.
	seen := map[int]bool{}
	for i := 0; i < len(brailleSpinnerFrames); i++ {
		seen[spinnerFrame(len(brailleSpinnerFrames), base.Add(time.Duration(i)*period), period)] = true
	}
	if len(seen) != len(brailleSpinnerFrames) {
		t.Errorf("one pass visited %d of %d frames", len(seen), len(brailleSpinnerFrames))
	}
}

func TestSpinnerIntervalClamp(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero is static", 0, 0},
		{"negative is static", -time.Second, 0},
		{"below the floor is raised", 10 * time.Millisecond, minSpinnerInterval},
		{"just below the floor", minSpinnerInterval - time.Millisecond, minSpinnerInterval},
		{"at the floor", minSpinnerInterval, minSpinnerInterval},
		{"the default is kept", defaultSpinnerInterval, defaultSpinnerInterval},
		{"a slow spinner is kept", 250 * time.Millisecond, 250 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSpinnerInterval(tc.in); got != tc.want {
				t.Errorf("normalizeSpinnerInterval(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	// A static interval has no phase and no tick, whatever the clock says.
	now := time.Unix(1_700_000_000, 0)
	if got := spinnerTick(now, 0); got != 0 {
		t.Errorf("static spinnerTick = %d, want 0", got)
	}
	if got := spinnerFrame(10, now, 0); got != 0 {
		t.Errorf("static spinnerFrame = %d, want 0", got)
	}
	if got := spinnerTickDelay(now, 0); got != 0 {
		t.Errorf("static spinnerTickDelay = %v, want 0", got)
	}
}

func TestSpinnerTickDelayLandsOnTheNextBoundary(t *testing.T) {
	const period = 80 * time.Millisecond
	base := time.Unix(1_700_000_000, 0)

	cases := []struct {
		name string
		at   time.Duration
		want time.Duration
	}{
		{"a quarter into the period", 20 * time.Millisecond, 60 * time.Millisecond},
		{"halfway", 40 * time.Millisecond, 40 * time.Millisecond},
		{"on a boundary waits one full period", 80 * time.Millisecond, period},
		{"just before a boundary lands on it", 79 * time.Millisecond, 1 * time.Millisecond},
		{"mid-way through a later period", 3*period + 20*time.Millisecond, 60 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := base.Add(tc.at)
			got := spinnerTickDelay(now, period)
			if got != tc.want {
				t.Fatalf("spinnerTickDelay = %v, want %v", got, tc.want)
			}
			// The whole contract: now+delay must be a multiple of the period.
			landing := now.Add(got).UnixNano()
			if landing%int64(period) != 0 {
				t.Errorf("landing at %v is not a multiple of %v", now.Add(got), period)
			}
		})
	}

	// The floor interval is what the chain actually uses.
	if got := spinnerTickDelay(base.Add(10*time.Millisecond), 5*time.Millisecond); got != minSpinnerInterval-10*time.Millisecond {
		t.Errorf("a sub-floor interval was not clamped: delay = %v", got)
	}
}

func TestSpinnerGlyphIsStaticUnderReducedMotion(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m.motion = MotionSettings{Mode: MotionReduced, SmoothStreaming: true}
	base := time.Unix(1_700_000_000, 0)

	for _, offset := range []time.Duration{0, 37 * time.Millisecond, 80 * time.Millisecond, 5 * time.Second} {
		if got := m.spinnerGlyph(base.Add(offset)); got != brailleSpinnerFrames[0] {
			t.Fatalf("reduced motion glyph at %v = %q, want the static first frame", offset, got)
		}
	}
	if got := m.spinnerEvery(); got != staticSpinnerRefresh {
		t.Errorf("static spinner period = %v, want the one-second label refresh", got)
	}

	// With motion on, the glyph must actually advance.
	m.motion = MotionSettings{Mode: MotionAuto}
	if m.spinnerGlyph(base) == m.spinnerGlyph(base.Add(defaultSpinnerInterval)) {
		t.Error("the animated glyph did not advance after one period")
	}
	if got := m.spinnerEvery(); got != defaultSpinnerInterval {
		t.Errorf("animated spinner period = %v, want %v", got, defaultSpinnerInterval)
	}
}

func TestSpinnerChainStartsAndStopsWithLiveSpinners(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.animEpoch = 3

	if cmd := m.armSpinnerTick(); cmd != nil {
		t.Fatal("an idle session armed a spinner tick; it would redraw forever")
	}

	m.streaming = true
	cmd := m.armSpinnerTick()
	if cmd == nil {
		t.Fatal("a live turn did not arm the shared spinner chain")
	}
	if again := m.armSpinnerTick(); again != nil {
		t.Fatal("a second chain was armed; timers would stack per chunk")
	}

	// The tick carries its turn's epoch and re-arms while something spins.
	msg, ok := cmd().(spinnerTickMsg)
	if !ok {
		t.Fatalf("the chain produced %T, want spinnerTickMsg", msg)
	}
	if msg.epoch != 3 {
		t.Fatalf("tick epoch = %d, want 3", msg.epoch)
	}
	next, _ := m.Update(msg)
	m = next.(Model)
	if !m.spinnerArmed {
		t.Fatal("the tick did not re-arm while the turn was still live")
	}

	// A stale tick (previous turn) must not keep a chain alive.
	m.spinnerArmed = false
	m.animEpoch = 9
	next, _ = m.Update(spinnerTickMsg{at: time.Now(), epoch: 8})
	m = next.(Model)
	if m.spinnerArmed {
		t.Fatal("a tick from a finished turn re-armed the chain")
	}

	// Turn over: the next tick stops for good.
	m.streaming = false
	m.spinnerArmed = false
	next, _ = m.Update(spinnerTickMsg{at: time.Now(), epoch: m.animEpoch})
	m = next.(Model)
	if m.spinnerArmed {
		t.Fatal("the chain survived the end of the turn")
	}
	if cmd := m.armSpinnerTick(); cmd != nil {
		t.Fatal("a finished turn re-armed the spinner chain")
	}
}

// A coalescing flush tick already repaints every widget, so the spinner chain
// must not arm beside it: one timer at a time.
func TestSpinnerChainDefersToAnInFlightFlush(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true
	m.flushArmed = true

	if cmd := m.armSpinnerTick(); cmd != nil {
		t.Fatal("the spinner chain armed on top of a live flush tick")
	}
	m.flushArmed = false
	if cmd := m.armSpinnerTick(); cmd == nil {
		t.Fatal("the spinner chain stayed silent after the flush tick finished")
	}
}

// --- reveal law -----------------------------------------------------------

func TestRevealStepLaw(t *testing.T) {
	cases := []struct {
		name    string
		backlog int
		want    int
	}{
		{"empty", 0, revealMinStep},
		{"one unit", 1, revealMinStep},
		{"exactly the minimum", 3, revealMinStep},
		{"just over the minimum", 4, revealMinStep},
		{"one catch-up frame", 8, revealMinStep},
		{"ceil(9/8)", 9, revealMinStep},
		{"ceil(24/8) is the minimum", 24, 3},
		{"ceil(25/8)", 25, 4},
		{"a burst", 80, 10},
		{"a wall of text", 800, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := revealStep(tc.backlog); got != tc.want {
				t.Errorf("revealStep(%d) = %d, want %d", tc.backlog, got, tc.want)
			}
		})
	}
}

func TestRevealCatchesUpSubLinearly(t *testing.T) {
	target := strings.Repeat("a", 800)
	shown := 0
	previous := 0
	for i := 0; i < revealCatchupFrames; i++ {
		shown = nextRevealStop(target, shown)
		if shown <= previous {
			t.Fatalf("frame %d revealed nothing", i)
		}
		previous = shown
		if shown > len(target) {
			t.Fatalf("frame %d ran past the target", i)
		}
	}
	// A catch-up, not a replay: eight frames consume about two thirds of the
	// burst (0.875^8 ≈ 0.34 remains), and the rest decays geometrically.
	if remaining := len(target) - shown; remaining > len(target)*2/5 {
		t.Fatalf("%d of %d units still hidden after %d frames", remaining, len(target), revealCatchupFrames)
	}

	frames := 0
	for shown < len(target) {
		shown = nextRevealStop(target, shown)
		frames++
		if frames > 100 {
			t.Fatal("the reveal never reached the end of the target")
		}
	}
	if shown != len(target) {
		t.Fatalf("reveal stopped at %d of %d", shown, len(target))
	}
}

func TestRevealNeverSplitsCombiningMarks(t *testing.T) {
	// Four clusters, each a base plus a combining acute accent.
	target := "e\u0301e\u0301e\u0301e\u0301"
	for shown := 0; shown < len(target); {
		next := nextRevealStop(target, shown)
		if next <= shown {
			t.Fatalf("no progress from offset %d", shown)
		}
		if next > len(target) {
			t.Fatalf("stop %d is past the target", next)
		}
		prefix := target[:next]
		if next < len(target) && strings.HasSuffix(prefix, "e") {
			t.Fatalf("offset %d split a base from its mark: %q", next, prefix)
		}
		shown = next
	}

	// Marks must not be counted as units of their own, or an accented word
	// would look twice as long to the pacer.
	if got := revealBacklog(target, 0); got != 4 {
		t.Errorf("revealBacklog of 4 clusters = %d", got)
	}
}

func TestRevealSnapsWhenPacingIsOff(t *testing.T) {
	const text = "one two three four five six seven eight nine ten"

	cases := []struct {
		name   string
		motion MotionSettings
	}{
		{"smooth streaming disabled", MotionSettings{Mode: MotionAuto, SmoothStreaming: false}},
		{"reduced motion", MotionSettings{Mode: MotionReduced, SmoothStreaming: true}},
		{"motion off", MotionSettings{Mode: MotionOff, SmoothStreaming: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forceMotionAll(t)
			m := newTestModel(nil)
			m.motion = tc.motion
			m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			m.streaming = true
			m = drive(m, agentChunkMsg{delta: text})

			tail := m.messages[len(m.messages)-1]
			if tail.Content != text {
				t.Fatalf("tail = %q, want the whole chunk immediately", tail.Content)
			}
			if m.smoothActive() {
				t.Error("pacing is active although the gate says it should not be")
			}
			if m.revealPending() {
				t.Error("a snapped buffer still reports a backlog")
			}
		})
	}
}

func TestStreamedTextIsPacedWhileStreaming(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.textarea.SetValue("go")
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.streaming {
		t.Fatal("test setup: the turn is not streaming")
	}

	m = drive(m, agentChunkMsg{delta: strings.Repeat("x", 80)})
	// The chunk lands in the buffer but must not appear yet.
	if len(m.messages) > 0 && strings.Contains(m.messages[len(m.messages)-1].Content, "x") {
		t.Fatal("the burst reached the transcript before a tick")
	}
	if !m.revealPending() {
		t.Fatal("a buffered burst reported no backlog")
	}

	m = drive(m, transcriptFlushMsg{})
	revealed := len([]rune(m.messages[len(m.messages)-1].Content))
	if want := revealStep(80); revealed != want {
		t.Fatalf("first tick revealed %d units, want %d", revealed, want)
	}

	// Growth is monotone and eventually complete.
	last := revealed
	for i := 0; i < 40 && m.revealPending(); i++ {
		m = drive(m, transcriptFlushMsg{})
		got := len([]rune(m.messages[len(m.messages)-1].Content))
		if got < last {
			t.Fatalf("reveal shrank from %d to %d", last, got)
		}
		last = got
	}
	if got := m.messages[len(m.messages)-1].Content; got != strings.Repeat("x", 80) {
		t.Fatalf("reveal finished with %d units, want 80", len([]rune(got)))
	}
}

// A turn end is discrete: the last frame of the turn carries the whole answer,
// never a half-revealed prefix.
func TestTurnEndSnapsTheReveal(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true
	m = drive(m, agentChunkMsg{delta: strings.Repeat("word ", 40)})
	m = drive(m, transcriptFlushMsg{})
	if !m.revealPending() {
		t.Fatal("test setup: the reveal had already finished")
	}

	m = drive(m, agentDoneMsg{})

	tail := m.messages[len(m.messages)-1]
	if tail.Role != RoleAssistant {
		t.Fatalf("last message is %v, want the assistant tail", tail.Role)
	}
	if tail.Content != strings.Repeat("word ", 40) {
		t.Fatalf("turn end left a partial reveal: %d runes", len([]rune(tail.Content)))
	}
	if m.revealPending() {
		t.Error("a finished turn still reports a reveal backlog")
	}
}

// --- tool cards -----------------------------------------------------------

func TestToolCardShowsSharedSpinnerAndElapsed(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true
	now := time.Unix(1_700_000_000, 0)

	m = drive(m, agentToolStartMsg{name: "file_write", args: `{"path":"a.go"}`, toolCallID: "c1"})
	m = revealAll(t, m)
	head, body, _ := strings.Cut(m.messages[0].Content, "\n")
	glyph := string([]rune(head)[0])
	if !isBrailleFrame(glyph) {
		t.Fatalf("card head %q does not start with a spinner frame", head)
	}
	if !strings.Contains(head, "running…") {
		t.Errorf("card head lost the intent text: %q", head)
	}
	if strings.Contains(head, "s ·") || strings.Contains(head, " · ") {
		t.Errorf("a fresh card already shows an elapsed badge: %q", head)
	}
	if body != `{"path":"a.go"}` {
		t.Errorf("card preview = %q", body)
	}

	// Same instant, same frame: the status bar and the card cannot disagree.
	m.pendingTools["c1"].since = now
	m.refreshLiveContent(now, false)
	if got, want := string([]rune(m.messages[0].Content)[0:1]), m.spinnerGlyph(now); got != want {
		t.Fatalf("card glyph %q differs from the bar's %q at the same instant", got, want)
	}

	// A second of runtime adds the elapsed badge, formatted like the status bar.
	m.pendingTools["c1"].since = now.Add(-3 * time.Second)
	m.refreshLiveContent(now, false)
	if got := m.messages[0].Content; !strings.Contains(got, " · 3s") {
		t.Errorf("card has no elapsed badge: %q", got)
	}

	// The ticker is what keeps it moving, so a later sample must re-render.
	m.pendingTools["c1"].since = now.Add(-65 * time.Second)
	next, _ := m.Update(spinnerTickMsg{at: now, epoch: m.animEpoch})
	m = next.(Model)
	if got := m.messages[0].Content; !strings.Contains(got, " · 1m05s") {
		t.Errorf("elapsed badge did not refresh on the shared tick: %q", got)
	}
}

func TestToolCardContentFormat(t *testing.T) {
	cases := []struct {
		name    string
		glyph   string
		elapsed time.Duration
		preview string
		want    string
	}{
		{"no preview", "⠋", 0, "", "⠋ running…"},
		{"sub-second has no badge", "⠙", 900 * time.Millisecond, "", "⠙ running…"},
		{"one second adds the badge", "⠹", time.Second, "", "⠹ running… · 1s"},
		{"minutes keep the seconds field", "⠸", 65 * time.Second, "", "⠸ running… · 1m05s"},
		{"preview goes on its own line", "⠼", 0, ` {"a":1} `, "⠼ running…\n{\"a\":1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolCardContent(toolCardRunning, tc.glyph, tc.elapsed, tc.preview); got != tc.want {
				t.Errorf("toolCardContent = %q, want %q", got, tc.want)
			}
		})
	}

	// Pending is the argument-streaming phase: same glyph and badge shape, a
	// state word that says what the card is waiting for.
	if got, want := toolCardContent(toolCardPending, "⠋", 0, ""), "⠋ pending…"; got != want {
		t.Errorf("pending card = %q, want %q", got, want)
	}
	if got := toolCardContent(toolCardPending, "⠋", 2*time.Second, `{"a":1}`); !strings.Contains(got, "⠋ pending… · 2s") {
		t.Errorf("pending card lost its badge or preview: %q", got)
	}

	long := toolCardContent(toolCardRunning, "⠋", 0, strings.Repeat("é", 500))
	preview := strings.SplitN(long, "\n", 2)[1]
	if n := len([]rune(preview)); n != toolPreviewRunes {
		t.Errorf("preview is %d runes, want %d", n, toolPreviewRunes)
	}
	if !strings.HasSuffix(preview, "...") {
		t.Error("an over-long preview was not ellipsized")
	}
}

// --- shimmer --------------------------------------------------------------

func TestShimmerTierBoundaries(t *testing.T) {
	// Head at cell 10 of a 30-cell label: 1/3 s of travel at 30 cells/s.
	now := time.Unix(0, 333_333_333)
	const length = 30

	cases := []struct {
		name string
		cell int
		want int
	}{
		{"at the head is the bright tier", 10, 2},
		{"one cell out stays bright", 9, 2},
		{"two cells out is still bright", 8, 2},
		{"three cells out is mid", 7, 1},
		{"four cells out is mid", 6, 1},
		{"five cells out is dim", 5, 0},
		{"at the band edge is dim", 4, 0},
		{"outside the band is dim", 0, 0},
		{"past the head is symmetric", 13, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shimmerTier(tc.cell, length, now); got != tc.want {
				t.Errorf("shimmerTier(cell %d) = %d, want %d", tc.cell, got, tc.want)
			}
		})
	}

	// Out-of-range cells never index a style.
	if got := shimmerTier(-1, length, now); got != 0 {
		t.Errorf("negative cell = %d, want dim", got)
	}
	if got := shimmerTier(length, length, now); got != 0 {
		t.Errorf("cell past the label = %d, want dim", got)
	}
	if got := shimmerTier(0, 0, now); got != 0 {
		t.Errorf("empty label = %d, want dim", got)
	}
}

// Fixed velocity, not fixed duration: a long label sweeps at the same speed as
// a short one.
func TestShimmerVelocityIsIndependentOfLength(t *testing.T) {
	const step = 500 * time.Millisecond
	base := time.Unix(1_700_000_000, 0)

	for _, length := range []int{10, 40, 200} {
		before := shimmerPosition(length, base)
		after := shimmerPosition(length, base.Add(step))
		travel := after - before
		if travel < 0 {
			travel += float64(length + 2*shimmerPadding)
		}
		if want := shimmerCellsPerSecond * step.Seconds(); travel != want {
			t.Errorf("length %d travelled %v cells in %v, want %v", length, travel, step, want)
		}
	}
}

func TestShimmerOpensOneStylePerRun(t *testing.T) {
	now := time.Unix(0, 400_000_000) // head at cell 12 of the cycle
	const label = "working progress"

	runs := shimmerRuns(label, now)
	if len(runs) == 0 {
		t.Fatal("no shimmer runs for a non-empty label")
	}
	units := len([]rune(label))
	if len(runs) >= units {
		t.Fatalf("%d runs for %d cells: the band is not coalescing", len(runs), units)
	}

	// Every cell is accounted for exactly once, in order.
	var joined strings.Builder
	for _, run := range runs {
		joined.WriteString(run.text)
	}
	if joined.String() != label {
		t.Errorf("runs reassemble to %q, want %q", joined.String(), label)
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].tier == runs[i-1].tier {
			t.Errorf("adjacent runs %d and %d share a tier", i-1, i)
		}
	}

	// One open and one reset per run: two escape sequences, not two per cell.
	rendered := renderShimmer(label, now)
	if seqs := strings.Count(rendered, "\x1b["); seqs != 2*len(runs) {
		t.Errorf("%d ANSI sequences for %d runs; styling must be per run, not per character", seqs, len(runs))
	}
	if got := stripANSI(rendered); got != label {
		t.Errorf("styled label reads %q, want %q", got, label)
	}
	if got := animVisibleWidth(rendered); got != units {
		t.Errorf("styled label is %d cells, want %d", got, units)
	}
}

// The shimmer is decoration: under reduced motion the same text renders in one
// plain run and the row keeps its width.
func TestShimmerStopsUnderReducedMotion(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m.width = 100
	m.streaming = true
	m.mascot.setState(mascotWorking, time.Now())
	now := time.Unix(1_700_000_000, 0)

	m.motion = MotionSettings{Mode: MotionAuto, SmoothStreaming: true}
	animated := m.renderWorkingState(now)
	if !strings.Contains(animated, "\x1b[") {
		t.Fatal("the animated working row carries no styling")
	}

	m.motion = MotionSettings{Mode: MotionReduced}
	static := m.renderWorkingState(now)
	// Without the band the row is one styled run, so it must carry strictly
	// fewer sequences than the tiered version.
	if strings.Count(static, "\x1b[") >= strings.Count(animated, "\x1b[") {
		t.Errorf("reduced motion still paints a band: %q", static)
	}
	if got, want := animVisibleWidth(animated), animVisibleWidth(static); got != want {
		t.Errorf("working row width changes with motion: %d animated vs %d static", got, want)
	}
	if got := stripANSI(static); !strings.Contains(got, "working") {
		t.Errorf("the state word disappeared under reduced motion: %q", got)
	}
}

// --- motion gate ----------------------------------------------------------

func TestMotionGatePrecedence(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		reduced bool
		want    motionLevel
	}{
		{"default is auto", "", false, motionFull},
		{"auto on a capable terminal", MotionAuto, false, motionFull},
		{"auto honours the system signal", MotionAuto, true, motionReduced},
		{"auto honours a lower-case signal", " AUTO ", true, motionReduced},
		{"reduced beats a capable terminal", MotionReduced, false, motionReduced},
		{"reduced stays reduced", MotionReduced, true, motionReduced},
		{"off beats reduced", MotionOff, true, motionOff},
		{"off beats a capable terminal", MotionOff, false, motionOff},
		{"unknown modes fall back to auto", "wobbly", true, motionReduced},
		{"unknown modes fall back to auto", "wobbly", false, motionFull},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+fmt.Sprint(tc.reduced), func(t *testing.T) {
			got := MotionSettings{Mode: tc.mode}.resolve(tc.reduced)
			if got != tc.want {
				t.Errorf("resolve(%q, reduced=%v) = %v, want %v", tc.mode, tc.reduced, got, tc.want)
			}
		})
	}
}

// Off is the strictest setting, and it still leaves a working indicator: a
// spinner glyph, the state word and un-paced text.
func TestMotionOffKeepsFunctionalIndicators(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m.motion = MotionSettings{Mode: MotionOff, SmoothStreaming: true}
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.textarea.SetValue("go")
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drive(m, agentChunkMsg{delta: "no pacing please"})

	if m.animating {
		t.Error("motion off still animates the mascot")
	}
	bar := stripANSI(m.renderStatusBar())
	if !strings.Contains(bar, "thinking") && !strings.Contains(bar, "working") {
		t.Errorf("the state word vanished under motion off: %q", bar)
	}
	if !strings.Contains(bar, brailleSpinnerFrames[0]) {
		t.Errorf("the spinner glyph vanished under motion off: %q", bar)
	}
	if tail := m.messages[len(m.messages)-1]; tail.Content != "no pacing please" {
		t.Errorf("streamed text was paced under motion off: %q", tail.Content)
	}
}

// --- mouse ----------------------------------------------------------------

func TestMouseWheelScrollsTranscript(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	for i := 0; i < 60; i++ {
		m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: fmt.Sprintf("history %d", i)})
	}
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset()
	if bottom == 0 {
		t.Fatal("test setup: the transcript does not scroll")
	}

	m = drive(m, tea.MouseWheelMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseWheelUp}))
	if got, want := m.viewport.YOffset(), bottom-mouseWheelLines; got != want {
		t.Fatalf("one wheel notch up moved the viewport to %d, want %d", got, want)
	}
	if m.viewport.AtBottom() {
		t.Error("scrolling up left the viewport pinned to the bottom")
	}

	m = drive(m, tea.MouseWheelMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseWheelDown}))
	if got := m.viewport.YOffset(); got != bottom {
		t.Fatalf("one wheel notch down did not return to the bottom: %d, want %d", got, bottom)
	}

	// The widget must not scroll a second time for the same notch.
	if m.viewport.MouseWheelEnabled {
		t.Error("the viewport still handles the wheel; each notch would scroll twice")
	}
}

// A wheel scroll must not be undone by the next streamed flush: the autoscroll
// contract follows only a viewport that was already at the bottom.
func TestMouseWheelScrollSurvivesStreaming(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	for i := 0; i < 40; i++ {
		m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: fmt.Sprintf("history %d", i)})
	}
	m.viewport.GotoBottom()

	m = drive(m, tea.MouseWheelMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseWheelUp}))
	parked := m.viewport.YOffset()

	m = drive(m, agentChunkMsg{delta: "more text arrives"})
	m = drive(m, transcriptFlushMsg{})
	if got := m.viewport.YOffset(); got != parked {
		t.Fatalf("streaming moved a scrolled-up viewport %d→%d", parked, got)
	}
}
