package tui

import (
	"testing"
	"time"
)

// A mascot that never blinks reads as a dead drawing. The blink was armed only
// by setState, which no-ops when the state is unchanged, so a session that
// stayed idle from startup to shutdown never blinked once.
func TestMascotEventuallyBlinksWithoutAnyStateChange(t *testing.T) {
	start := nowForTest()
	realNow := msgNow
	msgNow = func() time.Time { return start }
	defer func() { msgNow = realNow }()

	m := newMascot()

	blinked := false
	// Sample across two full blink periods; the mascot is stepped far more
	// often than the 4s cycle because frame rate and blink period differ.
	for i := 0; i < 400; i++ {
		msgNow = func() time.Time { return start.Add(time.Duration(i) * 20 * time.Millisecond) }
		m.step()
		if m.faceFor(msgNow()).left != faces[mascotIdle].left {
			blinked = true
			break
		}
	}

	if !blinked {
		t.Fatal("mascot never blinked across two full blink periods without a state change")
	}
}

// The blink must be a small fraction of the cycle, not a permanently shut face.
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
	// 200 frames at 20ms is 4s, exactly one period with a 110ms closure: 6
	// frames. Allow generous slack for sampling boundaries.
	if closedFrames == 0 {
		t.Error("blink never fired")
	}
	if closedFrames > total/4 {
		t.Errorf("eyes closed for %d of %d frames; the mascot looks asleep", closedFrames, total)
	}
}
