package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// contracts: the one shared clock (animclock.go), the deterministic scramble
// primitive (scramble.go), render-on-arrival as the shipped default, the motion
// gate, visible-only refresh and whole-frame mascot stepping. Every timing
// assertion drives the fake msgNow clock, so nothing here waits on a timer or
// depends on the host clock's phase.

// freezeClock pins msgNow and returns a setter to advance the fake instant.
func freezeClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	orig := msgNow
	now := at
	msgNow = func() time.Time { return now }
	t.Cleanup(func() { msgNow = orig })
	return func(at time.Time) { now = at }
}

// --- clock math -------------------------------------------------------------

func TestM5ClockFrameMath(t *testing.T) {
	if clockFPS != 20 || clockInterval != 50*time.Millisecond || clockLostAfter != 2*time.Second {
		t.Fatalf("clock constants drifted: fps=%d interval=%v lostAfter=%v", clockFPS, clockInterval, clockLostAfter)
	}
	base := time.Unix(1_700_000_000, 0) // a whole second sits on a 50ms boundary
	if got, want := clockFrame(base), base.UnixNano()/int64(clockInterval); got != want {
		t.Errorf("clockFrame(base) = %d, want %d", got, want)
	}
	if clockFrame(base.Add(49*time.Millisecond)) != clockFrame(base) {
		t.Error("49ms into a frame advanced the phase; frames would be uneven")
	}
	if clockFrame(base.Add(clockInterval)) != clockFrame(base)+1 {
		t.Error("one full interval did not advance the phase by exactly one")
	}
}

func TestM5ClockNoIdleTick(t *testing.T) {
	forceMotionAll(t)
	freezeClock(t, time.Unix(1_700_000_000, 0))
	m := newTestModel(nil)

	if m.clockLive() {
		t.Fatal("an idle session reports a live clock")
	}
	if cmd := m.armClockTick(); cmd != nil {
		t.Error("an idle session armed a tick; it would redraw forever")
	}

	m.animating = true
	if !m.clockLive() {
		t.Fatal("an animating turn does not own the clock")
	}
	if cmd := m.armClockTick(); cmd == nil {
		t.Error("an animating turn did not arm the clock")
	}
}

func TestM5ClockDedup(t *testing.T) {
	forceMotionAll(t)
	base := time.Unix(1_700_000_000, 0)
	advance := freezeClock(t, base)
	m := newTestModel(nil)
	m.streaming = true

	if cmd := m.armClockTick(); cmd == nil {
		t.Fatal("a live turn did not arm the clock")
	}
	seq := m.clockSeq
	// Still inside the lost-tick window: a burst of events must not stack chains.
	advance(base.Add(clockLostAfter - time.Millisecond))
	if again := m.armClockTick(); again != nil {
		t.Error("a second tick stacked on the outstanding one")
	}
	if m.clockSeq != seq {
		t.Error("a deduped arm bumped the sequence; it would supersede the live tick")
	}
}

func TestM5ClockLostTickRecovery(t *testing.T) {
	forceMotionAll(t)
	base := time.Unix(1_700_000_000, 0)
	advance := freezeClock(t, base)
	m := newTestModel(nil)
	m.streaming = true
	m.animEpoch = 5

	if cmd := m.armClockTick(); cmd == nil {
		t.Fatal("a live turn did not arm the clock")
	}
	oldSeq := m.clockSeq

	// Past the window the outstanding tick is presumed lost: a fresh arm
	// supersedes its seq so a late arrival can never double-drive the clock.
	advance(base.Add(clockLostAfter + time.Millisecond))
	if cmd := m.armClockTick(); cmd == nil {
		t.Fatal("lost-tick recovery did not re-arm")
	}
	if m.clockSeq <= oldSeq {
		t.Fatal("recovery did not supersede the lost tick's seq")
	}
	// The recovery window restarts from the new arm: no stacked replacements.
	advance(base.Add(clockLostAfter + 500*time.Millisecond))
	if again := m.armClockTick(); again != nil {
		t.Error("recovery stacked a second replacement inside its own window")
	}
	if m.acceptClockTick(animTickMsg{at: base, epoch: 5, seq: oldSeq}) {
		t.Error("the superseded tick was accepted; it would double-drive the clock")
	}
	if !m.acceptClockTick(animTickMsg{at: msgNow(), epoch: 5, seq: m.clockSeq}) {
		t.Error("the recovery tick was dropped")
	}
}

func TestM5ClockStaleEpochDropped(t *testing.T) {
	forceMotionAll(t)
	freezeClock(t, time.Unix(1_700_000_000, 0))
	m := newTestModel(nil)
	m.streaming = true
	m.animEpoch = 2
	m.armClockTick()

	stale := animTickMsg{at: msgNow(), epoch: 1, seq: m.clockSeq}
	if m.acceptClockTick(stale) {
		t.Error("a tick from a finished turn was accepted")
	}
	if !m.clockArmed {
		t.Error("a stale tick consumed the live latch")
	}
	m.clockArmed = false
	if _, cmd := m.Update(stale); cmd != nil {
		t.Error("a stale tick re-armed the clock; chains would accumulate")
	}
}

func TestM5AcceptClockTickConsumesLatch(t *testing.T) {
	forceMotionAll(t)
	base := time.Unix(1_700_000_000, 0)
	freezeClock(t, base)
	m := newTestModel(nil)
	m.streaming = true
	m.armClockTick()
	if !m.clockArmed {
		t.Fatal("setup: arming did not set the latch")
	}

	at := base.Add(clockInterval)
	if !m.acceptClockTick(animTickMsg{at: at, epoch: m.animEpoch, seq: m.clockSeq}) {
		t.Fatal("the live tick was dropped")
	}
	if m.clockArmed {
		t.Error("accept did not consume the latch; the next arm would be deduped forever")
	}
	if !m.lastTickAt.Equal(at) {
		t.Error("accept did not stamp lastTickAt with the tick's timestamp")
	}
	if cmd := m.armClockTick(); cmd == nil {
		t.Error("the consumed latch blocked the next arm")
	}
}

// --- scramble determinism ---------------------------------------------------

func TestM5ScrambleBirthDeterministic(t *testing.T) {
	for col := 0; col < 40; col++ {
		a, b := scrambleBirthStep(col), scrambleBirthStep(col)
		if a != b {
			t.Errorf("column %d born at %d then %d; birth must be pure", col, a, b)
		}
		if a < 0 || a >= scrambleBirthWindow {
			t.Errorf("column %d birth step %d outside [0,%d)", col, a, scrambleBirthWindow)
		}
	}
	// Seven is coprime to twenty: the first window of columns spreads across
	// every step instead of clustering.
	seen := map[int]bool{}
	for col := 0; col < scrambleBirthWindow; col++ {
		seen[scrambleBirthStep(col)] = true
	}
	if len(seen) != scrambleBirthWindow {
		t.Errorf("birth steps cluster: %d distinct steps for %d columns", len(seen), scrambleBirthWindow)
	}
}

func TestM5ScrambleFramesReproducible(t *testing.T) {
	for _, width := range []int{scrambleDefaultWidth, scrambleTranscriptWidth, 7} {
		frames := scrambleFrames(width)
		if len(frames) != scrambleFrameCount {
			t.Fatalf("width %d produced %d frames, want %d", width, len(frames), scrambleFrameCount)
		}
		again := buildScrambleFrames(width)
		for k, row := range frames {
			if row != again[k] {
				t.Errorf("width %d frame %d is not reproducible: %q vs %q", width, k, row, again[k])
			}
			if len([]rune(row)) != width {
				t.Errorf("width %d frame %d is %d runes", width, k, len([]rune(row)))
			}
			for _, r := range row {
				if !strings.ContainsRune(scrambleCharset, r) {
					t.Errorf("width %d frame %d rune %q outside the charset", width, k, r)
				}
			}
		}
		if frames[0] == frames[1] {
			t.Errorf("width %d successive frames are identical; the loop would not scramble", width)
		}
	}
}

func TestM5ScrambleInitializedThreshold(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		age  time.Duration
		want bool
	}{
		{0, false},
		{19 * clockInterval, false},
		{scrambleInitializedAge * clockInterval, true},
		{5 * time.Second, true},
	}
	for _, tc := range cases {
		if got := scrambleInitialized(now.Add(-tc.age), now); got != tc.want {
			t.Errorf("scrambleInitialized at age %v = %v, want %v", tc.age, got, tc.want)
		}
	}
	if !scrambleInitialized(time.Time{}, now) {
		t.Error("a zero start must read as initialized, not as the zero Time's 292 years")
	}
}

func TestM5SpinnerEllipsisCycle(t *testing.T) {
	// Four steps, one every 8 frames, then the cycle repeats.
	for step := 0; step < 2*len(ellipsisCycle); step++ {
		age := int64(step * ellipsisFramesPerStep)
		want := ellipsisCycle[step%len(ellipsisCycle)]
		if got := spinnerEllipsis(age, false, true); got != want {
			t.Errorf("ellipsis at age %d = %q, want %q", age, got, want)
		}
		if got := spinnerEllipsis(age+ellipsisFramesPerStep-1, false, true); got != want {
			t.Errorf("ellipsis moved inside its 8-frame step at age %d: %q", age, got)
		}
	}
	// The elapsed badge replaces the ellipsis entirely.
	for age := int64(0); age < 40; age++ {
		if got := spinnerEllipsis(age, true, true); got != "" {
			t.Errorf("ellipsis %q survived the elapsed suffix at age %d", got, age)
		}
	}
	// Under the motion gate it pins to the first cycle frame.
	if got := spinnerEllipsis(3*ellipsisFramesPerStep, false, false); got != ellipsisCycle[0] {
		t.Errorf("static ellipsis = %q, want %q", got, ellipsisCycle[0])
	}
}

// --- reveal default ---------------------------------------------------------

func TestM5RevealDefaultOff(t *testing.T) {
	forceMotionAll(t)
	text := strings.Repeat("tok", 20)

	t.Run("default renders on arrival", func(t *testing.T) {
		m := newTestModel(nil)
		m.motion = MotionSettings{Mode: MotionAuto, SmoothStreaming: false} // the shipped default
		m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
		m.streaming = true
		m.viewportFlushes = 0

		for i := 0; i < 5; i++ {
			m = drive(m, agentChunkMsg{delta: text})
		}
		tail := m.messages[len(m.messages)-1]
		if tail.Content != strings.Repeat(text, 5) {
			t.Fatalf("the burst did not land on arrival: %d runes", len([]rune(tail.Content)))
		}
		if m.revealPending() {
			t.Error("render-on-arrival reported a reveal backlog")
		}
		if m.viewportFlushes != 0 {
			t.Fatalf("chunks flushed the viewport %d times; the shared tick coalesces", m.viewportFlushes)
		}

		m = drive(m, animTickMsg{at: msgNow(), epoch: m.animEpoch, seq: m.clockSeq})
		if m.viewportFlushes != 1 {
			t.Fatalf("the coalescing tick applied %d updates, want 1", m.viewportFlushes)
		}
		if !strings.Contains(stripANSI(m.viewport.GetContent()), "toktoktoktoktok") {
			t.Error("the flushed frame lost the burst")
		}
	})

	t.Run("opt-in still paces", func(t *testing.T) {
		m := newTestModel(nil) // testMotion opts in: SmoothStreaming=true
		m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
		m.streaming = true
		m = drive(m, agentChunkMsg{delta: strings.Repeat("x", 80)})

		if len(m.messages) > 0 && strings.Contains(m.messages[len(m.messages)-1].Content, "x") {
			t.Fatal("the burst reached the transcript before a tick")
		}
		if !m.revealPending() {
			t.Fatal("a buffered burst reported no backlog")
		}
		m = drive(m, animTickMsg{at: msgNow(), epoch: m.animEpoch, seq: m.clockSeq})
		if got, want := len([]rune(m.messages[len(m.messages)-1].Content)), revealStep(80); got != want {
			t.Fatalf("first tick revealed %d units, want %d", got, want)
		}
	})
}

// --- motion gate ------------------------------------------------------------

func TestM5MotionGateStaticDataRefreshes(t *testing.T) {
	forceMotionAll(t)
	now := time.Unix(1_700_000_000, 0)

	for _, mode := range []string{MotionReduced, MotionOff} {
		t.Run(mode, func(t *testing.T) {
			m := newTestModel(nil)
			m.motion = MotionSettings{Mode: mode, SmoothStreaming: true}
			m.streaming = true
			m.turnStarted = now.Add(-5 * time.Second)

			if m.decorativeMotion() {
				t.Fatal("the gate reports decorative motion")
			}
			if got := m.clockEvery(); got != staticSpinnerRefresh {
				t.Errorf("clock period = %v, want the one-second data refresh", got)
			}

			// Static row: the same cells at every instant, no gradient.
			row := m.barSpinnerGlyphs(now)
			if row != scrambleFrames(barSpinnerWidth)[0] {
				t.Errorf("row = %q, want the static first frame", row)
			}
			if row != m.barSpinnerGlyphs(now.Add(37*time.Millisecond)) {
				t.Error("the glyph row advanced under the gate")
			}
			if strings.Contains(row, "\x1b[") {
				t.Error("the static row carries a decorative gradient")
			}
			// The ellipsis pins to its first cycle frame.
			head := stripANSI(m.toolSpinnerHead(toolCardRunning, now, now, 0))
			if !strings.HasSuffix(head, "running.") {
				t.Errorf("the ellipsis cycles under the gate: %q", head)
			}

			// Elapsed badges are data, not decoration: they keep refreshing.
			m.messages = []ChatMessage{{Role: RoleTool, Label: "🔧 file_write", Content: ""}}
			m.pendingTools["c1"] = &toolCard{
				index: 0, since: now.Add(-65 * time.Second),
				args: "{}", shown: 2, status: toolCardRunning,
			}
			m.refreshLiveContent(now, false)
			if !strings.Contains(m.messages[0].Content, " · 1m05s") {
				t.Errorf("the elapsed badge froze under the gate: %q", m.messages[0].Content)
			}
		})
	}
}

// --- visible-only refresh ---------------------------------------------------

func TestM5ItemVisibleViewportOnly(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	for i := 0; i < 60; i++ {
		m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: fmt.Sprintf("history %d", i)})
	}
	m.viewport.GotoBottom()

	last := len(m.messages) - 1
	if !m.itemVisible(last) {
		t.Error("the bottom item is not visible with the viewport at the bottom")
	}
	if m.itemVisible(0) {
		t.Error("an off-screen item counted as visible; it would spend renders nobody sees")
	}

	// Unmeasured items count as visible: the spans lag a just-appended message.
	m.messages = append(m.messages, ChatMessage{Role: RoleUser, Label: "You", Content: "just appended"})
	if !m.itemVisible(len(m.messages) - 1) {
		t.Error("an unmeasured item was frozen instead of refreshed")
	}
	if m.itemVisible(-1) || m.itemVisible(len(m.messages)) {
		t.Error("an out-of-range index counted as visible")
	}

	// Scrolling back into view resumes the item on the next tick.
	m.viewport.GotoTop()
	if !m.itemVisible(0) {
		t.Error("scrolling back into view did not make the item visible again")
	}
}

// --- mascot stepping --------------------------------------------------------

func TestM5MascotStepDueWholeFrames(t *testing.T) {
	cases := []struct {
		state mascotState
		every int64
	}{
		{mascotWorking, 1},  // fps20: every shared frame
		{mascotThinking, 2}, // fps10: every second frame
		{mascotIdle, 4},     // fps5: every fourth frame
		{mascotSuccess, 4},
		{mascotError, 4},
	}
	for _, tc := range cases {
		for frame := int64(0); frame < 40; frame++ {
			if got, want := mascotStepDue(tc.state, frame), frame%tc.every == 0; got != want {
				t.Errorf("mascotStepDue(%v, %d) = %v, want %v", tc.state, frame, got, want)
			}
		}
	}
}
