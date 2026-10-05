package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Walks a whole turn; mascot was once dropped by narrow-terminal fallback.
func TestMascotSurvivesAWholeTurn(t *testing.T) {
	const model = "hf.co/dealignai/Ornith-1.5-9B-UNCENSORED-GGUF:Q4_K_M"

	m := NewModel("ollama", model, "v0.6.1", "D:/CODING/GoCode", nil,
		make(chan string, 4), make(chan tea.Msg, 64), make(chan struct{}), nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	bar := func() string { return stripANSI(m.renderStatusBar()) }
	face := func() string { return strings.TrimSpace(m.mascot.inline(msgNow(), mascotFaceStyles)) }

	if got := face(); got == "" {
		t.Fatal("no mascot in the status bar at startup")
	}

	faces := map[string]string{"idle": face()}

	m.textarea.SetValue("hi lol")
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	faces["thinking"] = face()
	if !m.streaming {
		t.Error("Enter did not put the model into streaming state")
	}
	if !strings.Contains(bar(), "thinking") {
		t.Errorf("status bar does not report the thinking state: %q", bar())
	}

	m = drive(m, agentChunkMsg{delta: "Hey! What can I help you with today?"})
	faces["working"] = face()

	m = drive(m, agentDoneMsg{})
	faces["done"] = face()
	if m.animating {
		t.Error("animation kept running after the turn ended")
	}

	// Mascot must be on bar at every stage, not just some.
	for _, stage := range []string{"idle", "thinking", "working", "done"} {
		if strings.TrimSpace(faces[stage]) == "" {
			t.Errorf("mascot missing from the status bar while %s", stage)
		}
	}

	// Each state should be visually distinct.
	seen := map[string]string{}
	for stage, f := range faces {
		if other, dup := seen[f]; dup {
			t.Errorf("%s and %s render the same face %q", other, stage, f)
		}
		seen[f] = stage
	}
}

// Drive real frames and count distinct spring positions.
func TestMascotActuallyMovesWhileWorking(t *testing.T) {
	m := NewModel("ollama", "llama3", "v0.6.1", "D:/CODING/GoCode", nil,
		make(chan string, 4), make(chan tea.Msg, 64), make(chan struct{}), nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.textarea.SetValue("go")
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drive(m, agentChunkMsg{delta: "working"})

	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	bob, sway, bars, sprites := map[int]bool{}, map[int]bool{}, map[string]bool{}, map[string]bool{}

	for i := 0; i < 400; i++ {
		m = drive(m, frameMsg{
			at:    start.Add(time.Duration(i) * 50 * time.Millisecond),
			epoch: m.animEpoch,
		})
		bob[int(m.mascot.bob.value()*1000)] = true
		sway[int(m.mascot.sway.value()*1000)] = true
		bars[m.renderStatusBar()] = true
		sprites[m.mascot.inline(start, mascotFaceStyles)] = true
	}

	if len(bob) < 8 {
		t.Errorf("bob spring only visited %d distinct positions over 400 frames; it is not moving", len(bob))
	}
	if len(sway) < 8 {
		t.Errorf("sway spring only visited %d distinct positions over 400 frames; it is not moving", len(sway))
	}
	// Rendered bar must change, not merely spring values; blink can't carry this.
	if len(bars) < 2 {
		t.Errorf("the rendered status bar never changed across 400 frames of working (%d distinct)",
			len(bars))
	}
	// Same for sprite alone, isolating it from bar padding.
	if len(sprites) < 2 {
		t.Errorf("the mascot sprite never changed across 400 frames of working (%d distinct)",
			len(sprites))
	}
}

func drive(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}
