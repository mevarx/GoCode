package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Stale ticks must not re-arm the clock or chains accumulate.
func TestStaleFrameDoesNotRearmTicker(t *testing.T) {
	m := newTestModel(make(chan struct{}, 1))

	// Turn 1 starts, then ends while a tick is still in flight.
	m.animating = true
	m.animEpoch = 1
	m.animating = false

	// Turn 2 starts before that stale tick lands.
	m.animating = true
	m.animEpoch = 2

	stale := animTickMsg{at: time.Now(), epoch: 1, seq: m.clockSeq}
	_, cmd := m.Update(stale)

	if cmd != nil {
		t.Error("a tick from a previous turn re-armed the clock; chains would accumulate")
	}
}

func TestCurrentFrameRearmsTicker(t *testing.T) {
	m := newTestModel(make(chan struct{}, 1))
	m.animating = true
	m.animEpoch = 7

	_, cmd := m.Update(animTickMsg{at: time.Now(), epoch: 7, seq: m.clockSeq})
	if cmd == nil {
		t.Error("the current turn's tick must re-arm the clock or the mascot freezes mid-stream")
	}
}

// Idle must not tick: no frame, no redraw, no CPU.
func TestIdleFrameDoesNotRearmTicker(t *testing.T) {
	m := newTestModel(make(chan struct{}, 1))
	m.animating = false
	m.animEpoch = 3

	_, cmd := m.Update(animTickMsg{at: time.Now(), epoch: 3, seq: m.clockSeq})
	if cmd != nil {
		t.Error("an idle session re-armed the clock; it would redraw forever")
	}
}

// Every turn must advance epoch or stale frames match later turns.
func TestEnterAdvancesEpoch(t *testing.T) {
	m := newTestModel(make(chan struct{}, 1))
	m.textarea.SetValue("do a thing")
	before := m.animEpoch

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	if m.animEpoch == before {
		t.Fatal("starting a turn did not advance animEpoch")
	}
	if !m.animating {
		t.Error("starting a turn must set animating")
	}
	if m.mascot.state != mascotThinking {
		t.Errorf("state = %v, want thinking before the first token", m.mascot.state)
	}
}

func TestDoneStopsAnimation(t *testing.T) {
	m := newTestModel(make(chan struct{}, 1))
	m.animating = true
	m.streaming = true
	m.mascot.setState(mascotWorking, msgNow())

	next, _ := m.Update(agentDoneMsg{})
	m = next.(Model)

	if m.animating {
		t.Error("a finished turn must stop the ticker")
	}
	if m.mascot.state != mascotSuccess {
		t.Errorf("state = %v, want done after a clean turn", m.mascot.state)
	}
}

// Cancelled turn is user action, not failure.
func TestCancelledTurnIsNotAnError(t *testing.T) {
	m := newTestModel(make(chan struct{}, 1))
	m.animating = true
	m.streaming = true

	next, _ := m.Update(agentDoneMsg{err: context.Canceled})
	m = next.(Model)

	if m.mascot.state == mascotError {
		t.Error("a cancelled turn showed the error face; interrupting is a user action")
	}
	if m.animating {
		t.Error("a cancelled turn must still stop the ticker")
	}
}
