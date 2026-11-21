package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// focusState is the focus enum. focusNone is the
// pre-ready state: no pane owns the keyboard until the first WindowSizeMsg.
type focusState int

const (
	focusNone focusState = iota
	focusEditor
	focusMain
	focusSidebar
)

func (f focusState) String() string {
	switch f {
	case focusEditor:
		return "editor"
	case focusMain:
		return "main"
	case focusSidebar:
		return "sidebar"
	}
	return "none"
}

// setFocus moves the keyboard focus and keeps the textarea's own focus flag in
// sync, so history recall and completion (which test textarea.Focused) agree
// with the enum.
func (m *Model) setFocus(f focusState) {
	m.focus = f
	if f == focusEditor {
		m.textarea.Focus()
	} else {
		m.textarea.Blur()
	}
}

// handleKey is the dispatch contract, in order:
//
// 1. ctrl+c BEFORE dialogs — except approval keeps ctrl+c = deny+quit (a
// security invariant GoCode preserves), and the quit dialog's own ctrl+c
// quits.
// 2. every other key goes to the open dialog first.
// 3. esc while a turn runs cancels it.
// 4. focus routing: editor (prompt + @popup), main (chat), sidebar.
func (m *Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		switch {
		case m.overlays.has(overlayApproval):
			return m.updateApproval(msg)
		case m.overlays.has(overlayQuit):
			return m.updateQuit(msg)
		case m.streaming:
			return m.cancelOrQuit()
		default:
			m.openQuitDialog()
			return *m, nil
		}
	}

	if m.overlays.has(overlayApproval) {
		return m.updateApproval(msg)
	}
	if kind, ok := m.overlays.top(); ok {
		switch kind {
		case overlayPalette:
			return m.updatePalette(msg)
		case overlayPicker:
			return m.updatePicker(msg)
		case overlaySession:
			return m.updateSessionPicker(msg)
		case overlayQuit:
			return m.updateQuit(msg)
		case overlayTheme:
			return m.updateThemePicker(msg)
		case overlayThemeEditor:
			return m.updateThemeEditor(msg)
		}
	}

	if key == "esc" && m.streaming {
		m.closeFileCompletion()
		return m.cancelOrQuit()
	}

	switch m.focus {
	case focusMain:
		return m.handleChatKey(msg)
	case focusSidebar:
		return m.handleSidebarKey(msg)
	default:
		return m.handleEditorKey(msg)
	}
}

// cancelOrQuit is the shared streaming interrupt: first press cancels the turn,
// a second press quits. Both ctrl+c and esc route here while busy.
func (m *Model) cancelOrQuit() (Model, tea.Cmd) {
	if m.cancelRequested {
		return *m, tea.Quit
	}
	m.cancelRequested = true
	m.interruptTurn()
	return *m, nil
}

// handleEditorKey routes the prompt surface (focus editor, or none pre-ready).
// The @file popup owns keys while open; otherwise the keymap is tried and any
// unmatched key falls through to the textarea.
func (m *Model) handleEditorKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.fileComp.open {
		return *m, m.updateFileCompletion(msg)
	}

	// Any key other than Tab abandons an in-progress command completion cycle.
	if msg.String() != "tab" {
		m.clearCompletion()
	}

	contexts := []keyContext{keyCtxIdle}
	if m.streaming {
		contexts = []keyContext{keyCtxStreaming}
	}
	contexts = append(contexts, keyCtxGlobal)

	if binding, ok := lookupBindingIn(msg.String(), contexts...); ok {
		switch binding.Action {
		case actOpenPicker:
			return *m, m.openModelPicker()
		case actPaletteOpen:
			m.openCommandPalette()
			return *m, nil
		case actThemeOpen:
			m.openThemePicker()
			return *m, nil
		case actFollowBottom:
			m.followBottom()
			return *m, nil
		case actComplete:
			// Tab completes slash commands (complete.go); on any other prompt
			// it cycles focus to the transcript.
			if strings.HasPrefix(m.textarea.Value(), "/") {
				m.handleTabCompletion()
				return *m, nil
			}
			m.setFocus(focusMain)
			return *m, nil
		case actHistoryPrev:
			m.recallHistory(true)
			return *m, nil
		case actHistoryNext:
			m.recallHistory(false)
			return *m, nil
		case actRecallPrev:
			if m.textarea.Focused() && m.textarea.Value() == "" {
				m.recallHistory(true)
				return *m, nil
			}
		case actRecallNext:
			if m.textarea.Focused() && m.caretAtEnd() {
				m.recallHistory(false)
				return *m, nil
			}
		case actCancelOrQuit:
			return m.cancelOrQuit()
		case actQuit:
			m.openQuitDialog()
			return *m, nil
		case actClearInput:
			// esc restores the pre-navigation draft while recalling history;
			// otherwise it clears the prompt (legacy behaviour).
			if m.promptHistory.navigating() {
				m.setText(m.promptHistory.restoreDraft())
				return *m, nil
			}
			if m.textarea.Value() != "" {
				m.textarea.Reset()
			}
			return *m, nil
		case actSubmit:
			return *m, m.submitPrompt()
		case actSteer:
			m.steerPrompt()
			return *m, nil
		case actScrollPageUp:
			m.viewport.ScrollUp(m.viewport.Height() / 2)
			return *m, nil
		case actScrollPageDn:
			m.viewport.ScrollDown(m.viewport.Height() / 2)
			return *m, nil
		case actScrollUp:
			m.viewport.ScrollUp(3)
			return *m, nil
		case actScrollDown:
			m.viewport.ScrollDown(3)
			return *m, nil
		}
	}

	return *m, m.forwardEditorKey(msg)
}

// forwardEditorKey hands an unmatched key to the textarea and maintains the
// @file popup: a typed '@' may open it, every other edit re-filters or closes it.
func (m *Model) forwardEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.focus != focusEditor && m.focus != focusNone {
		return nil
	}
	var cmds []tea.Cmd
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(msg)
	cmds = append(cmds, taCmd)
	if text, ok := printableInput(msg); ok && text == "@" {
		if cmd := m.maybeOpenFileCompletion(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	m.syncFileCompletion()
	return tea.Batch(cmds...)
}

// caretAtEnd reports whether the cursor sits at the end of the prompt text
// (logical line/column, independent of soft wrap). Down recalls history only
// from here.
func (m *Model) caretAtEnd() bool {
	lines := strings.Split(m.textarea.Value(), "\n")
	last := len(lines) - 1
	return m.textarea.Line() == last && m.textarea.Column() == len([]rune(lines[last]))
}

// followBottom scrolls to the transcript bottom, selects the last item and
// re-arms follow (a viewport at the bottom follows subsequent growth).
func (m *Model) followBottom() {
	m.viewport.GotoBottom()
	if n := len(m.messages); n > 0 {
		m.focusedItem = n - 1
	}
}

// handleChatKey routes the transcript surface (focus=main). Chat keys win over
// globals; pgup/pgdown page the transcript here rather than the editor's half
// page. Unmatched keys are swallowed — the textarea must not see them.
func (m *Model) handleChatKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.String()
	if b, ok := lookupBinding(keyCtxChat, key); ok {
		switch b.Action {
		case actChatLineUp:
			m.chatLineScroll(-1)
			return *m, nil
		case actChatLineDown:
			m.chatLineScroll(1)
			return *m, nil
		case actChatItemPrev:
			m.chatSelectItem(-1)
			return *m, nil
		case actChatItemNext:
			m.chatSelectItem(1)
			return *m, nil
		case actChatHalfUp:
			m.chatHalfPage(-1)
			return *m, nil
		case actChatHalfDown:
			m.chatHalfPage(1)
			return *m, nil
		case actChatPageUp:
			m.chatPage(-1)
			return *m, nil
		case actChatPageDown:
			m.chatPage(1)
			return *m, nil
		case actChatTop:
			m.chatTop()
			return *m, nil
		case actChatBottom:
			m.chatBottom()
			return *m, nil
		case actChatExpand:
			m.chatExpand()
			return *m, nil
		case actChatCopy:
			return *m, m.chatCopy()
		case actChatClearSel:
			m.chatClearSelection()
			return *m, nil
		case actChatHorizLeft:
			m.chatHoriz(-1)
			return *m, nil
		case actChatHorizRight:
			m.chatHoriz(1)
			return *m, nil
		case actFocusSidebar:
			// The sidebar is only a focus target when it is actually shown.
			if !calculateLayout(m.width, m.height).compact {
				m.setFocus(focusSidebar)
			}
			return *m, nil
		case actFocusEditor:
			m.setFocus(focusEditor)
			return *m, nil
		}
	}
	if b, ok := lookupBinding(keyCtxGlobal, key); ok {
		switch b.Action {
		case actOpenPicker:
			return *m, m.openModelPicker()
		case actPaletteOpen:
			m.openCommandPalette()
			return *m, nil
		case actThemeOpen:
			m.openThemePicker()
			return *m, nil
		case actFollowBottom:
			m.followBottom()
			return *m, nil
		case actScrollPageUp:
			m.chatPage(-1)
			return *m, nil
		case actScrollPageDn:
			m.chatPage(1)
			return *m, nil
		}
	}
	return *m, nil
}

// handleSidebarKey routes the sidebar surface (focus=sidebar): four-line steps,
// top/bottom, and back to chat (h/left) or editor (tab).
func (m *Model) handleSidebarKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.String()
	if b, ok := lookupBinding(keyCtxSidebar, key); ok {
		switch b.Action {
		case actSidebarUp:
			m.scrollSidebar(-sidebarScrollStep)
			return *m, nil
		case actSidebarDown:
			m.scrollSidebar(sidebarScrollStep)
			return *m, nil
		case actSidebarTop:
			m.sidebarOffset = 0
			return *m, nil
		case actSidebarBot:
			m.sidebarOffset = m.sidebarMaxOffset()
			return *m, nil
		case actFocusMain:
			m.setFocus(focusMain)
			return *m, nil
		case actFocusEditor:
			m.setFocus(focusEditor)
			return *m, nil
		}
	}
	if b, ok := lookupBinding(keyCtxGlobal, key); ok {
		switch b.Action {
		case actOpenPicker:
			return *m, m.openModelPicker()
		case actPaletteOpen:
			m.openCommandPalette()
			return *m, nil
		case actThemeOpen:
			m.openThemePicker()
			return *m, nil
		case actFollowBottom:
			m.followBottom()
			return *m, nil
		}
	}
	return *m, nil
}
