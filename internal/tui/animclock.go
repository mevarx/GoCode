package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// The one clock: every animated surface — transcript
// flush coalescing, spinner scramble, elapsed badges, the opt-in reveal pacer
// and the mascot's springs — rides a single generation-guarded 20 fps tick.
// No primitive schedules itself.
const (
	// clockFPS is the shared frame rate; 1s/20 = 50ms per frame.
	clockFPS = 20
	// clockInterval is the shared frame interval.
	clockInterval = time.Second / clockFPS
	// clockLostAfter bounds how long an armed tick may stay silent while
	// something is live. Past it the outstanding tick is presumed lost and a
	// fresh one is armed.
	clockLostAfter = 2 * time.Second
)

// animTickMsg is the single shared animation tick. epoch drops ticks armed by
// a finished turn; seq drops ticks superseded by lost-tick recovery, so even a
// late arrival from the same turn can never double-drive the clock.
type animTickMsg struct {
	at    time.Time
	epoch int
	seq   int
}

// clockFrame is floor(now/50ms): the shared phase index every spinner reads,
// so all of them agree about the frame without talking to each other.
func clockFrame(now time.Time) int64 {
	return spinnerTick(now, clockInterval)
}

// clockEvery is the live tick interval: the 50ms frame while decorative
// motion runs, and the one-second data refresh when it does not. Elapsed
// badges are data, not decoration, so they keep moving under the motion gate.
func (m *Model) clockEvery() time.Duration {
	if m.decorativeMotion() {
		return clockInterval
	}
	return staticSpinnerRefresh
}

// clockLive reports whether anything needs the clock at all. A turn owns the
// clock while it streams; once it ends the session is settled history and the
// clock freezes — an idle session arms nothing (the no-idle-tick contract).
func (m *Model) clockLive() bool {
	return m.streaming || m.animating
}

// armClockTick arms the single shared tick. It is deduped so a burst of
// events cannot stack chains: while a tick is outstanding and younger than
// the lost-tick window, arming is a no-op. Past the window the outstanding
// tick is presumed lost and superseded — its seq will no longer match, so a
// late arrival is ignored instead of doubling the chain.
func (m *Model) armClockTick() tea.Cmd {
	if !m.clockLive() {
		return nil
	}
	now := msgNow()
	if m.clockArmed && now.Sub(m.lastTickAt) < clockLostAfter {
		return nil
	}
	m.clockArmed = true
	m.clockSeq++
	// The recovery window restarts from the arm, so repeated arming inside a
	// lost-tick window cannot stack replacements either.
	m.lastTickAt = now
	epoch, seq := m.animEpoch, m.clockSeq
	delay := spinnerTickDelay(now, m.clockEvery())
	return tea.Tick(delay, func(t time.Time) tea.Msg {
		return animTickMsg{at: t, epoch: epoch, seq: seq}
	})
}

// acceptClockTick reports whether a tick belongs to the live chain and marks
// it consumed. Stale generations and superseded sequences are dropped whole:
// they neither rewrite content nor re-arm.
func (m *Model) acceptClockTick(msg animTickMsg) bool {
	if msg.epoch != m.animEpoch || msg.seq != m.clockSeq {
		return false
	}
	m.clockArmed = false
	m.lastTickAt = msg.at
	return true
}

// mascotStepDue reports whether the mascot advances on this clock frame. The
// springs run at fps20; states whose cadence is slower than the shared frame
// step on whole-frame multiples of it, so unifying the tick source keeps
// their feel without a second chain.
func mascotStepDue(s mascotState, frame int64) bool {
	every := int64(cadence(s) / clockInterval)
	if every < 1 {
		every = 1
	}
	return frame%every == 0
}
