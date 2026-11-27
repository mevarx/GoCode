package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestScrollbackSurvivesDiscreteAgentEvents(t *testing.T) {
	events := []struct {
		name string
		msg  tea.Msg
	}{
		{"tool start", agentToolStartMsg{name: "read", toolCallID: "read-1"}},
		{"tool result", agentToolMsg{name: "read", result: "finished", toolCallID: "read-1"}},
		{"notice", agentToolMsg{name: "GoCode", result: "notice"}},
		{"turn end", agentDoneMsg{}},
	}
	for _, event := range events {
		for _, follow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/follow=%t", event.name, follow), func(t *testing.T) {
				m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 20})
				m.streaming = true
				for i := 0; i < 40; i++ {
					m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: fmt.Sprintf("history %d", i)})
				}
				if event.name == "tool result" {
					m = drive(m, agentToolStartMsg{name: "read", toolCallID: "read-1"})
				}
				m.viewport.GotoBottom()
				if !follow {
					m = drive(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
					if m.viewport.AtBottom() {
						t.Fatal("Page Up did not leave the bottom")
					}
				}
				parked := m.viewport.YOffset()
				m = drive(m, event.msg)
				if follow && !m.viewport.AtBottom() {
					t.Fatal("event stopped the viewport from following the bottom")
				}
				if !follow && m.viewport.YOffset() != parked {
					t.Fatalf("event moved scrollback from %d to %d", parked, m.viewport.YOffset())
				}
			})
		}
	}
}

func TestSubmittingPromptKeepsASingleOutputListener(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.textarea.SetValue("test prompt")
	m.outputCh <- agentDoneMsg{}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submit did not start the turn's animation clock")
	}
	// Init already has a pending read. The submit command must not batch a
	// second read with the clock, or the two workers can deliver events out of order.
	got := cmd()
	if _, ok := got.(animTickMsg); !ok {
		t.Fatalf("submit started work other than the tick: %T", got)
	}
}

func TestScrollbackSelectsTheVisibleItemAndPaintsItsGutter(t *testing.T) {
	m := m4ChatModel(30)
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyHome})
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if got := m.focusedItem; got != 1 {
		t.Fatalf("expected the second item, got %d", got)
	}
	lines := strings.Split(stripANSI(m.viewport.GetContent()), "\n")
	if !strings.HasPrefix(lines[2], "▌ ") || strings.HasPrefix(lines[0], "▌ ") {
		t.Fatalf("the visible gutter did not move to the selected item: %q", lines[:4])
	}
	m = drive(m, tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelDown}))
	if !m.itemVisible(m.focusedItem) {
		t.Fatal("wheel left the selected item outside the viewport")
	}
}

func TestTabFromEditorSelectsVisibleScrollback(t *testing.T) {
	m := m4ChatModel(30)
	m.setFocus(focusEditor)
	m.focusedItem = len(m.messages) - 1
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus != focusMain || !m.itemVisible(m.focusedItem) {
		t.Fatalf("Tab left chat focus=%s, selected %d outside scrollback", m.focus, m.focusedItem)
	}
}

func TestExpandingTallLastItemKeepsItsStartInView(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
	m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: strings.Repeat("a line\n", 50)})
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drive(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if !m.expanded[0] || m.viewport.YOffset() != 0 {
		t.Fatalf("expanding the selected message jumped to line %d", m.viewport.YOffset())
	}
}
