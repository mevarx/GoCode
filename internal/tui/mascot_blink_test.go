package tui

import (
	"testing"
	"time"
)

// Idle-only sessions never blinked since setState no-ops when unchanged.
func TestMascotEventuallyBlinksWithoutAnyStateChange(t *testing.T) {
	start := nowForTest()
	realNow := msgNow
	msgNow = func() time.Time { return start }
	defer func() { msgNow = realNow }()

	m := newMascot()

	blinked := false
	for i := 0; i < 400; i++ {
		msgNow = func() time.Time { return start.Add(time.Duration(i) * 20 * time.Millisecond) }
		m.step(msgNow())
		if m.faceFor(msgNow()).left != faces[mascotIdle].left {
			blinked = true
			break
		}
	}

	if !blinked {
		t.Fatal("mascot never blinked across two full blink periods without a state change")
	}
}

// Blink must be a small fraction of the cycle, not permanently shut.
func TestBlinkDoesNotStayClosed(t *testing.T) {
	start := nowForTest()
	m := newMascot()
	m.eyes.at = start

	closedFrames := 0
	const total = 200
	for i := 0; i < total; i++ {
		now := start.Add(time.Duration(i) * 20 * time.Millisecond)
		if m.eyes.isClosed(now) {
			closedFrames++
		}
	}
	// 200 frames at 20ms is 4s, exactly one period with 110ms closure.
	if closedFrames == 0 {
		t.Error("blink never fired")
	}
	if closedFrames > total/4 {
		t.Errorf("eyes closed for %d of %d frames; the mascot looks asleep", closedFrames, total)
	}
}
