package tui

import (
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/safetext"
)

// Status toasts: a transient
// message REPLACES the status/help row for a default 5 s TTL. There is no
// floating overlay. Toasts are sequence-stamped so a stale expiry can never
// clear a newer message, and expiry rides a one-shot tick — no perpetual idle
// tick (the P4 no-idle-tick contract).

type toastKind int

const (
	toastSuccess toastKind = iota
	toastInfo
	toastWarning
	toastError
)

// defaultToastTTL is the spec's DefaultStatusTTL.
const defaultToastTTL = 5 * time.Second

type toastState struct {
	kind    toastKind
	body    string
	seq     int
	expires time.Time
	set     bool
}

// toastExpiryMsg clears one toast by sequence stamp; a mismatch is stale.
type toastExpiryMsg struct{ seq int }

// showToast installs a transient message and arms its one-shot expiry. The
// injectable msgNow clock makes the TTL fake-clock testable.
func (m *Model) showToast(kind toastKind, body string) tea.Cmd {
	m.toastSeq++
	m.toast = toastState{
		kind:    kind,
		body:    safetext.Sanitize(body),
		seq:     m.toastSeq,
		expires: msgNow().Add(defaultToastTTL),
		set:     true,
	}
	seq := m.toastSeq
	return tea.Tick(defaultToastTTL, func(time.Time) tea.Msg {
		return toastExpiryMsg{seq: seq}
	})
}

// toastActive reports whether the toast is still within its TTL at now.
func (m *Model) toastActive(now time.Time) bool {
	return m.toast.set && now.Before(m.toast.expires)
}

// expireToast clears the toast only when the sequence stamp matches: a newer
// toast owns a newer stamp, so an old timer cannot wipe it.
func (m *Model) expireToast(seq int) {
	if m.toast.set && m.toast.seq == seq {
		m.toast = toastState{}
	}
}

// indicator maps the kind to its badge label and fill.
func (t toastState) indicator() (string, color.Color) {
	tk := activeTheme.Tokens
	switch t.kind {
	case toastSuccess:
		return "OKAY!", tk.Success
	case toastWarning:
		return "WARNING", tk.Warning
	case toastError:
		return "ERROR", tk.Destructive
	default:
		return "HEY!", tk.Info
	}
}

// flattenToastBody collapses newlines to spaces so the bar stays one row.
func flattenToastBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", " ")
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.ReplaceAll(body, "\r", " ")
	return body
}

// renderToast draws [INDICATOR][body padded to full width] as a solid bar.
func (m *Model) renderToast(width int) string {
	label, fill := m.toast.indicator()
	ind := badge(label, fill)
	bodyW := max(0, width-lipgloss.Width(ind))

	var padded string
	if bodyW >= 2 {
		inner := bodyW - 2
		body := truncateToWidth(flattenToastBody(m.toast.body), inner)
		padded = " " + body + strings.Repeat(" ", max(0, inner-lipgloss.Width(body))) + " "
	} else {
		padded = strings.Repeat(" ", bodyW)
	}
	bar := lipgloss.NewStyle().
		Background(activeTheme.Tokens.BgLeastVisible).
		Foreground(activeTheme.Tokens.FgBase).
		Render(padded)
	return ind + bar
}
