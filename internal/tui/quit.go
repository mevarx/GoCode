package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Quit confirmation: the safe default is Nope, and a
// second ctrl+c quits without confirmation. While a turn runs, ctrl+c keeps
// its existing cancel-first behaviour; the dialog only opens from idle.

const (
	quitFocusYep  = 0
	quitFocusNope = 1
)

func (m *Model) openQuitDialog() {
	m.overlays.push(overlayQuit)
	m.quitFocus = quitFocusNope
}

func (m *Model) closeQuitDialog() { m.overlays.pop(overlayQuit) }

func (m Model) updateQuit(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Second press: quit without confirmation, per the hint.
		return m, tea.Quit
	case "left", "right", "tab":
		if m.quitFocus == quitFocusYep {
			m.quitFocus = quitFocusNope
		} else {
			m.quitFocus = quitFocusYep
		}
	case "enter":
		if m.quitFocus == quitFocusYep {
			return m, tea.Quit
		}
		m.closeQuitDialog()
	case "y":
		return m, tea.Quit
	case "n", "esc":
		m.closeQuitDialog()
	}
	return m, nil
}

func (m *Model) renderQuitDialog() string {
	width := max(30, min(70, m.width-2))
	var b strings.Builder
	b.WriteString("Quit GoCode?")
	if m.streaming {
		warn := lipgloss.NewStyle().Foreground(activeTheme.Tokens.WarningSubtle).Render
		b.WriteString("\n" + warn("A turn is still running and will be cut off."))
	}
	b.WriteString("\n\n")
	b.WriteString(dialogButton("Yep!", m.quitFocus == quitFocusYep, false))
	b.WriteString("  ")
	b.WriteString(dialogButton("Nope", m.quitFocus == quitFocusNope, false))
	b.WriteString("\n\n")
	b.WriteString(dialogHelp([2]string{"y", "quit"}, [2]string{"n/esc", "stay"}, [2]string{"ctrl+c", "quit now"}))
	b.WriteString("\n" + systemStyle.Render("To quit without confirmation press ctrl+c twice."))
	return dialogFrame("Quit", dialogNormal, b.String(), width)
}
