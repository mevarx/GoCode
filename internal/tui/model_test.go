package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func newTestModel(cancelCh chan struct{}) Model {
	if cancelCh == nil {
		cancelCh = make(chan struct{}, 1)
	}
	return NewModel("ollama", "llama3", "0.4.0", "", NewApprovalBridge(), make(chan string, 1), make(chan string, 1), make(chan tea.Msg, 16), cancelCh, []list.Item{}, testMotion())
}

// testMotion is the production default: motion follows the terminal and
// streamed text is paced.
func testMotion() MotionSettings {
	return MotionSettings{Mode: MotionAuto, SmoothStreaming: true}
}

func cmdQuits(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestEscClearsInputAndDoesNotQuit(t *testing.T) {
	m := newTestModel(nil)
	m.textarea.SetValue("half-typed message")

	m2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmdQuits(t, cmd) {
		t.Fatal("Esc unexpectedly quit the program")
	}
	if got := m2.(Model).textarea.Value(); got != "" {
		t.Errorf("expected textarea cleared after Esc, got %q", got)
	}
}

func TestEscIdleDoesNotQuit(t *testing.T) {
	m := newTestModel(nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmdQuits(t, cmd) {
		t.Fatal("Esc with empty input unexpectedly quit the program")
	}
}

func TestCtrlCQuitsWhenIdle(t *testing.T) {
	m := newTestModel(nil)
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmdQuits(t, cmd) {
		t.Fatal("first Ctrl+C should open the quit confirmation, not quit")
	}
	m = next.(Model)
	if !m.overlays.has(overlayQuit) {
		t.Fatal("quit confirmation not open after Ctrl+C")
	}
	if m.quitFocus != quitFocusNope {
		t.Fatal("quit confirmation must default to the safe Nope button")
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !cmdQuits(t, cmd) {
		t.Fatal("second Ctrl+C should quit without confirmation")
	}
}

func TestCtrlCInterruptsStreamingTurn(t *testing.T) {
	cancelCh := make(chan struct{}, 1)
	m := newTestModel(cancelCh)
	m.streaming = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmdQuits(t, cmd) {
		t.Fatal("first Ctrl+C while streaming must interrupt, not quit")
	}
	select {
	case <-cancelCh:
	default:
		t.Fatal("expected interrupt signal on cancelCh")
	}
}

func TestWindowResizeFitsSmallTerminal(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 32, Height: 10})
	m = updated.(Model)
	if m.viewport.Width() != 28 || m.viewport.Height() != 2 {
		t.Fatalf("unexpected viewport dimensions: %dx%d", m.viewport.Width(), m.viewport.Height())
	}
	if got := lipgloss.Height(m.View().Content); got != 10 {
		t.Errorf("expected terminal-height layout of 10 lines, got %d", got)
	}
	for i, line := range strings.Split(m.View().Content, "\n") {
		if width := lipgloss.Width(line); width > 32 {
			t.Errorf("view line %d is %d cells wide in a 32-cell terminal", i, width)
		}
	}
}

func TestStatusBarShowsWorkspace(t *testing.T) {
	m := NewModel("deepseek", "deepseek-flash", "0.4.0", "/work/my-project", NewApprovalBridge(), make(chan string, 1), make(chan string, 1), make(chan tea.Msg, 16), make(chan struct{}, 1), []list.Item{}, testMotion())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	if got := m.renderStatusBar(); !strings.Contains(got, "my-project") {
		t.Fatalf("expected workspace name in status bar, got %q", got)
	}
	// The status bar lives inside the frame's one-cell side margins.
	if got, want := lipgloss.Width(m.renderStatusBar()), m.width-2; got != want {
		t.Fatalf("expected status bar width %d, got %d", want, got)
	}
}

func TestEscInterruptsStreamingTurn(t *testing.T) {
	cancelCh := make(chan struct{}, 1)
	m := newTestModel(cancelCh)
	m.streaming = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmdQuits(t, cmd) {
		t.Fatal("Esc while streaming must interrupt, not quit")
	}
	select {
	case <-cancelCh:
	default:
		t.Fatal("expected interrupt signal on cancelCh")
	}
}

func TestSecondCtrlCWhileStreamingQuits(t *testing.T) {
	cancelCh := make(chan struct{}, 1)
	m := newTestModel(cancelCh)
	m.streaming = true
	m.cancelRequested = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !cmdQuits(t, cmd) {
		t.Fatal("second Ctrl+C while streaming should quit")
	}
}

func TestEnterQuitAliases(t *testing.T) {
	for _, alias := range []string{"exit", "quit", "/exit", "/quit"} {
		m := newTestModel(nil)
		m.textarea.SetValue(alias)
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !cmdQuits(t, cmd) {
			t.Errorf("expected %q to quit the program", alias)
		}
	}
}
