package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/session"
)

// Session switcher states: normal → deleting (ctrl+x,
// y/enter confirms, n/esc cancels) and normal → renaming (ctrl+r, enter saves,
// esc cancels). The title bar recolours per mode; deleting the busy session is
// refused. Mutations run off the update loop and reload the listing after.

type sessionMode int

const (
	sessionNormal sessionMode = iota
	sessionDeleting
	sessionRenaming
)

// syncSessionPickerTitle repaints the title bar for the current mode.
func (m *Model) syncSessionPickerTitle() {
	switch m.sessionMode {
	case sessionDeleting:
		m.sessionPicker.Title = "Delete this session?"
		m.sessionPicker.Styles.Title = lipgloss.NewStyle().
			Foreground(activeTheme.Tokens.Destructive).Bold(true).
			Padding(0, 0, 1, pickerFramePad)
	case sessionRenaming:
		m.sessionPicker.Title = "Rename this session?"
		m.sessionPicker.Styles.Title = lipgloss.NewStyle().
			Foreground(activeTheme.Tokens.WarningSubtle).Bold(true).
			Padding(0, 0, 1, pickerFramePad)
	default:
		m.sessionPicker.Title = "Resume Session"
		m.sessionPicker.Styles.Title = pickerTitleStyle
	}
}

// sessionStoreCmd runs one store mutation off the update loop, then reloads
// the listing so the picker reflects the change. A missing store reports
// through the same message path as a failed listing.
func (m *Model) sessionStoreCmd(fn func(*session.SessionStore) error) tea.Cmd {
	sess := m.agentSession
	if sess == nil || sess.Store() == nil {
		return func() tea.Msg { return sessionListMsg{err: errSessionStoreUnavailable} }
	}
	store := sess.Store()
	return func() tea.Msg {
		if err := fn(store); err != nil {
			return sessionListMsg{err: err}
		}
		summaries, err := store.ListSessions(sessionListLimit)
		return sessionListMsg{summaries: summaries, err: err}
	}
}
