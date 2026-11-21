package tui

import (
	"encoding/base64"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// collapseLines is the collapsed height of a long transcript item: the first
// twelve lines plus a "space to expand" affordance,
// adapted to a single shared cap). The live streaming tail is never collapsed.
const collapseLines = 12

// horizStep is the horizontal scroll granularity for H/L (five cells per step,
// clamped to the longest line).
const horizStep = 5

// itemSpan locates one transcript item in the rendered line stream: start is
// its first line offset, lines its rendered height. renderMessages fills these
// so navigation can map viewport offsets to items without a second render.
type itemSpan struct {
	start int
	lines int
}

// chatSpans returns the current item geometry, refreshing the render cache (and
// the spans) first so a just-appended message is measurable.
func (m *Model) chatSpans() []itemSpan {
	if len(m.messages) == 0 {
		return nil
	}
	m.renderMessages()
	return m.spans
}

// firstVisibleItem is the first item with any line at or below the viewport top.
func (m *Model) firstVisibleItem() int {
	top := m.viewport.YOffset()
	for i, s := range m.spans {
		if s.start+s.lines > top {
			return i
		}
	}
	return len(m.spans) - 1
}

// lastVisibleItem is the last item whose first line is above the viewport bottom.
func (m *Model) lastVisibleItem() int {
	bottom := m.viewport.YOffset() + m.viewport.Height()
	last := 0
	for i, s := range m.spans {
		if s.start < bottom {
			last = i
		} else {
			break
		}
	}
	return last
}

// itemVisible reports whether transcript item i intersects the visible
// viewport. Unmeasured items (the spans lag a just-appended message) count as
// visible: refreshing a live card is cheaper than freezing it by mistake.
func (m *Model) itemVisible(i int) bool {
	if i < 0 || i >= len(m.messages) {
		return false
	}
	if !m.ready || i >= len(m.spans) {
		return true
	}
	s := m.spans[i]
	top := m.viewport.YOffset()
	return s.start+s.lines > top && s.start < top+m.viewport.Height()
}

// clampSelectionToView drags the highlight to the nearest visible item after a
// line/page scroll, so a selection never strands off-screen.
func (m *Model) clampSelectionToView() {
	if m.focusedItem < 0 || m.focusedItem >= len(m.spans) {
		return
	}
	top := m.viewport.YOffset()
	bottom := top + m.viewport.Height()
	s := m.spans[m.focusedItem]
	switch {
	case s.start+s.lines <= top:
		m.focusedItem = m.firstVisibleItem()
	case s.start >= bottom:
		m.focusedItem = m.lastVisibleItem()
	}
}

// scrollItemIntoView moves the viewport the minimum distance to show item i.
func (m *Model) scrollItemIntoView(i int) {
	if i < 0 || i >= len(m.spans) {
		return
	}
	s := m.spans[i]
	top := m.viewport.YOffset()
	h := m.viewport.Height()
	switch {
	case s.start < top:
		m.viewport.SetYOffset(max(0, s.start))
	case s.start+s.lines > top+h:
		m.viewport.SetYOffset(max(0, s.start+s.lines-h))
	}
}

func (m *Model) chatLineScroll(dir int) {
	if m.chatSpans() == nil {
		return
	}
	if dir < 0 {
		m.viewport.ScrollUp(1)
	} else {
		m.viewport.ScrollDown(1)
	}
	m.clampSelectionToView()
}

func (m *Model) chatSelectItem(dir int) {
	spans := m.chatSpans()
	if spans == nil {
		return
	}
	cur := m.focusedItem
	if cur < 0 {
		if dir > 0 {
			cur = -1
		} else {
			cur = len(spans)
		}
	}
	next := min(len(spans)-1, max(0, cur+dir))
	m.focusedItem = next
	m.scrollItemIntoView(next)
}

func (m *Model) chatHalfPage(dir int) {
	if m.chatSpans() == nil {
		return
	}
	if dir < 0 {
		m.viewport.HalfPageUp()
		m.focusedItem = m.firstVisibleItem()
	} else {
		m.viewport.HalfPageDown()
		m.focusedItem = m.lastVisibleItem()
	}
}

func (m *Model) chatPage(dir int) {
	if m.chatSpans() == nil {
		return
	}
	if dir < 0 {
		m.viewport.PageUp()
	} else {
		m.viewport.PageDown()
	}
	m.clampSelectionToView()
}

func (m *Model) chatTop() {
	if m.chatSpans() == nil {
		return
	}
	m.viewport.GotoTop()
	m.focusedItem = 0
}

func (m *Model) chatBottom() {
	spans := m.chatSpans()
	if spans == nil {
		return
	}
	m.viewport.GotoBottom()
	m.focusedItem = len(spans) - 1
}

// chatExpand toggles the selected item's collapsed/expanded state. Expanding
// while following re-pins the bottom so the revealed lines are not lost.
func (m *Model) chatExpand() {
	if m.focusedItem < 0 || m.focusedItem >= len(m.messages) {
		return
	}
	atBottom := m.viewport.AtBottom()
	if m.expanded == nil {
		m.expanded = make(map[int]bool)
	}
	m.expanded[m.focusedItem] = !m.expanded[m.focusedItem]
	m.flushNow()
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// chatClearSelection clears the highlight (esc clears the selection).
func (m *Model) chatClearSelection() {
	m.focusedItem = -1
	m.flushNow()
}

// chatHoriz scrolls the focused item horizontally by five cells, clamped to the
// longest line. Generalized beyond shell items (PRD "better" fix).
func (m *Model) chatHoriz(dir int) {
	i := m.focusedItem
	if i < 0 || i >= len(m.messages) {
		return
	}
	maxOff := m.itemMaxHorizOffset(m.messages[i])
	if m.hOffset == nil {
		m.hOffset = make(map[int]int)
	}
	m.hOffset[i] = horizClamp(m.hOffset[i], dir*horizStep, maxOff)
	m.flushNow()
}

// itemMaxHorizOffset is how far the item can scroll: the longest line minus the
// visible content width, never negative.
func (m *Model) itemMaxHorizOffset(msg ChatMessage) int {
	capWidth := min(120, max(1, m.chatContentWidth()-2))
	longest := 0
	for _, line := range strings.Split(strings.TrimRight(m.renderMessage(msg), "\n"), "\n") {
		if w := lipgloss.Width(line); w > longest {
			longest = w
		}
	}
	return max(0, longest-capWidth)
}

// horizClamp applies a signed delta to an offset, clamped to [0, maxOff].
func horizClamp(offset, delta, maxOff int) int {
	n := offset + delta
	if n < 0 {
		return 0
	}
	if n > maxOff {
		return maxOff
	}
	return n
}

// copyText is the clipboard payload for one item: its content, plus the diff
// when the item carries one.
func copyText(msg ChatMessage) string {
	if msg.Diff != "" {
		return msg.Content + "\n" + msg.Diff
	}
	return msg.Content
}

// chatCopy copies the selected item via OSC 52 and confirms with a toast. c/y
// and the formerly-dead C/Y all route here (central copy, PRD fix).
func (m *Model) chatCopy() tea.Cmd {
	i := m.focusedItem
	if i < 0 || i >= len(m.messages) {
		return nil
	}
	msg := m.messages[i]
	body := "Message copied to clipboard"
	if msg.Role == RoleTool {
		body = "Tool content copied to clipboard"
	}
	seq := osc52Sequence(copyText(msg))
	return tea.Batch(
		tea.Printf("%s", seq),
		m.showToast(toastSuccess, body),
	)
}

// osc52Sequence builds the OSC 52 clipboard escape: base64 payload, clipboard
// selection "c", ST terminator. Pure so it is testable without a terminal.
func osc52Sequence(content string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(content)) + "\x1b\\"
}

// collapseAffordance is the hint line appended to a collapsed long item.
func collapseAffordance(hidden int) string {
	return fmt.Sprintf("… (+%d lines, space to expand)", hidden)
}
