package tui

// Keybindings are data: one table drives dispatch, the help line and the
// documentation the audit keeps in sync. Contexts are explicit because the
// same key means different things while streaming, in a modal, or at the
// prompt — a context-scoped lookup is what keeps those meanings from leaking.
type keyContext int

const (
	// keyCtxGlobal binds work in every non-modal context.
	keyCtxGlobal keyContext = iota
	// keyCtxIdle is the prompt with no turn running.
	keyCtxIdle
	// keyCtxStreaming is the prompt while a turn is running.
	keyCtxStreaming
	// keyCtxTextarea is the input widget's own handling.
	keyCtxTextarea
	// keyCtxTranscript is viewport scrolling; Up/Down only apply there when
	// the textarea is not focused.
	keyCtxTranscript
	keyCtxApproval
	keyCtxPicker
	keyCtxPalette
	// keyCtxChat is the transcript with focus=main.
	keyCtxChat
	// keyCtxSidebar is the sidebar with focus=sidebar.
	keyCtxSidebar
)

type keyAction string

const (
	actOpenPicker    keyAction = "open-picker"
	actCloseOverlay  keyAction = "close-overlay"
	actPickerSelect  keyAction = "picker-select"
	actCancelOrQuit  keyAction = "cancel-or-quit"
	actQuit          keyAction = "quit"
	actClearInput    keyAction = "clear-input"
	actSubmit        keyAction = "submit"
	actSteer         keyAction = "steer"
	actNewline       keyAction = "newline"
	actScrollUp      keyAction = "scroll-up"
	actScrollDown    keyAction = "scroll-down"
	actScrollPageUp  keyAction = "scroll-page-up"
	actScrollPageDn  keyAction = "scroll-page-down"
	actComplete      keyAction = "complete"
	actHistoryPrev   keyAction = "history-prev"
	actHistoryNext   keyAction = "history-next"
	actRecallPrev    keyAction = "recall-prev"
	actRecallNext    keyAction = "recall-next"
	actPaletteOpen   keyAction = "palette-open"
	actPaletteClose  keyAction = "palette-close"
	actPaletteSelect keyAction = "palette-select"
	actPaletteUp     keyAction = "palette-up"
	actPaletteDown   keyAction = "palette-down"

	actApprovalToggle   keyAction = "approval-toggle"
	actApprovalConfirm  keyAction = "approval-confirm"
	actApprovalAllow    keyAction = "approval-allow"
	actApprovalReject   keyAction = "approval-reject"
	actApprovalGrant    keyAction = "approval-grant"
	actApprovalReadOnly keyAction = "approval-read-only"
	actApprovalDenyQuit keyAction = "approval-deny-quit"

	actApprovalDiffToggle     keyAction = "approval-diff-toggle"
	actApprovalDiffFullscreen keyAction = "approval-diff-fullscreen"

	actThemeOpen keyAction = "theme-open"

	// focus model.
	actFocusEditor  keyAction = "focus-editor"
	actFocusMain    keyAction = "focus-main"
	actFocusSidebar keyAction = "focus-sidebar"
	actFollowBottom keyAction = "follow-bottom"

	// chat keys (focus=main).
	actChatLineUp     keyAction = "chat-line-up"
	actChatLineDown   keyAction = "chat-line-down"
	actChatItemPrev   keyAction = "chat-item-prev"
	actChatItemNext   keyAction = "chat-item-next"
	actChatHalfUp     keyAction = "chat-half-up"
	actChatHalfDown   keyAction = "chat-half-down"
	actChatPageUp     keyAction = "chat-page-up"
	actChatPageDown   keyAction = "chat-page-down"
	actChatTop        keyAction = "chat-top"
	actChatBottom     keyAction = "chat-bottom"
	actChatExpand     keyAction = "chat-expand"
	actChatCopy       keyAction = "chat-copy"
	actChatClearSel   keyAction = "chat-clear-selection"
	actChatHorizLeft  keyAction = "chat-horiz-left"
	actChatHorizRight keyAction = "chat-horiz-right"

	// sidebar keys (focus=sidebar).
	actSidebarUp   keyAction = "sidebar-up"
	actSidebarDown keyAction = "sidebar-down"
	actSidebarTop  keyAction = "sidebar-top"
	actSidebarBot  keyAction = "sidebar-bottom"
)

// keyBinding is one row of the table. Keys are Bubble Tea key strings.
type keyBinding struct {
	Keys    []string
	Context keyContext
	Action  keyAction
	// Help is the help-line fragment; empty when the binding is not surfaced.
	Help string
}

// keyBindings covers every binding the TUI had before the table existed, plus
// the P6 additions (completion, history, palette). The audit's table is the
// contract: a test asserts every legacy key/context pair is still here.
var keyBindings = []keyBinding{
	// Prompt: idle.
	{Keys: []string{"ctrl+l"}, Context: keyCtxGlobal, Action: actOpenPicker, Help: "Ctrl+L models"},
	// ctrl+p is the canonical palette key; ctrl+k stays as a silent alias.
	{Keys: []string{"ctrl+p"}, Context: keyCtxGlobal, Action: actPaletteOpen, Help: "Ctrl+P commands"},
	{Keys: []string{"ctrl+k"}, Context: keyCtxGlobal, Action: actPaletteOpen, Help: ""},
	{Keys: []string{"ctrl+t"}, Context: keyCtxGlobal, Action: actThemeOpen, Help: "Ctrl+T themes"},
	// ctrl+end re-arms follow from any focus.
	{Keys: []string{"ctrl+end"}, Context: keyCtxGlobal, Action: actFollowBottom, Help: ""},
	{Keys: []string{"ctrl+c"}, Context: keyCtxIdle, Action: actQuit, Help: "Ctrl+C quit"},
	{Keys: []string{"esc"}, Context: keyCtxIdle, Action: actClearInput, Help: "Esc clear"},
	{Keys: []string{"enter"}, Context: keyCtxIdle, Action: actSubmit, Help: "Enter send"},
	{Keys: []string{"tab"}, Context: keyCtxIdle, Action: actComplete, Help: ""},
	{Keys: []string{"alt+p"}, Context: keyCtxIdle, Action: actHistoryPrev, Help: ""},
	{Keys: []string{"alt+n"}, Context: keyCtxIdle, Action: actHistoryNext, Help: ""},
	{Keys: []string{"up"}, Context: keyCtxIdle, Action: actRecallPrev, Help: ""},
	{Keys: []string{"down"}, Context: keyCtxIdle, Action: actRecallNext, Help: ""},

	// Prompt: streaming.
	{Keys: []string{"ctrl+c"}, Context: keyCtxStreaming, Action: actCancelOrQuit, Help: "Ctrl+C stop · press again to quit"},
	{Keys: []string{"esc"}, Context: keyCtxStreaming, Action: actCancelOrQuit, Help: "Esc stop"},
	{Keys: []string{"enter"}, Context: keyCtxStreaming, Action: actSteer, Help: ""},

	// Input widget.
	{Keys: []string{"shift+enter"}, Context: keyCtxTextarea, Action: actNewline, Help: "Shift+Enter newline"},

	// Transcript scrolling.
	{Keys: []string{"up"}, Context: keyCtxTranscript, Action: actScrollUp, Help: ""},
	{Keys: []string{"down"}, Context: keyCtxTranscript, Action: actScrollDown, Help: ""},

	// Scroll keys apply in every non-modal context.
	{Keys: []string{"pgup"}, Context: keyCtxGlobal, Action: actScrollPageUp, Help: "PgUp/PgDn scroll"},
	{Keys: []string{"pgdown"}, Context: keyCtxGlobal, Action: actScrollPageDn, Help: ""},

	// Model picker.
	{Keys: []string{"ctrl+l", "esc"}, Context: keyCtxPicker, Action: actCloseOverlay, Help: "Esc close"},
	{Keys: []string{"enter"}, Context: keyCtxPicker, Action: actPickerSelect, Help: "Enter select"},

	// Command palette.
	{Keys: []string{"ctrl+k", "ctrl+p", "esc"}, Context: keyCtxPalette, Action: actPaletteClose, Help: "Esc close"},
	{Keys: []string{"enter"}, Context: keyCtxPalette, Action: actPaletteSelect, Help: "Enter run"},
	{Keys: []string{"up"}, Context: keyCtxPalette, Action: actPaletteUp, Help: ""},
	{Keys: []string{"down", "ctrl+n"}, Context: keyCtxPalette, Action: actPaletteDown, Help: ""},

	// Approval modal. Approval always owns focus; these are its only keys.
	{Keys: []string{"left", "right", "tab"}, Context: keyCtxApproval, Action: actApprovalToggle, Help: "← → Tab switch"},
	{Keys: []string{"enter"}, Context: keyCtxApproval, Action: actApprovalConfirm, Help: "Enter confirm"},
	{Keys: []string{"y"}, Context: keyCtxApproval, Action: actApprovalAllow, Help: ""},
	{Keys: []string{"n"}, Context: keyCtxApproval, Action: actApprovalReject, Help: ""},
	{Keys: []string{"a"}, Context: keyCtxApproval, Action: actApprovalGrant, Help: ""},
	{Keys: []string{"r"}, Context: keyCtxApproval, Action: actApprovalReadOnly, Help: ""},
	{Keys: []string{"esc"}, Context: keyCtxApproval, Action: actApprovalReject, Help: ""},
	{Keys: []string{"ctrl+c"}, Context: keyCtxApproval, Action: actApprovalDenyQuit, Help: ""},
	{Keys: []string{"t"}, Context: keyCtxApproval, Action: actApprovalDiffToggle, Help: ""},
	{Keys: []string{"f"}, Context: keyCtxApproval, Action: actApprovalDiffFullscreen, Help: ""},

	// Chat / transcript (focus=main). space is Expand ONLY — paging is f/pgdown,
	// avoiding a double-binding. c/y/C/Y copy centrally; esc clears the
	// highlight; H/L scroll the focused item horizontally (PRD "better" fixes).
	{Keys: []string{"j", "down", "ctrl+j"}, Context: keyCtxChat, Action: actChatLineDown, Help: "↑↓ scroll"},
	{Keys: []string{"k", "up", "ctrl+k"}, Context: keyCtxChat, Action: actChatLineUp, Help: ""},
	{Keys: []string{"J", "shift+down"}, Context: keyCtxChat, Action: actChatItemNext, Help: "shift+↑↓ item"},
	{Keys: []string{"K", "shift+up"}, Context: keyCtxChat, Action: actChatItemPrev, Help: ""},
	{Keys: []string{"u"}, Context: keyCtxChat, Action: actChatHalfUp, Help: ""},
	{Keys: []string{"d"}, Context: keyCtxChat, Action: actChatHalfDown, Help: ""},
	{Keys: []string{"b", "pgup"}, Context: keyCtxChat, Action: actChatPageUp, Help: ""},
	{Keys: []string{"f", "pgdown"}, Context: keyCtxChat, Action: actChatPageDown, Help: ""},
	{Keys: []string{"g", "home"}, Context: keyCtxChat, Action: actChatTop, Help: ""},
	{Keys: []string{"G", "end"}, Context: keyCtxChat, Action: actChatBottom, Help: ""},
	{Keys: []string{"space"}, Context: keyCtxChat, Action: actChatExpand, Help: "space expand"},
	{Keys: []string{"c", "y", "C", "Y"}, Context: keyCtxChat, Action: actChatCopy, Help: "c/y copy"},
	{Keys: []string{"esc"}, Context: keyCtxChat, Action: actChatClearSel, Help: ""},
	{Keys: []string{"H", "shift+left"}, Context: keyCtxChat, Action: actChatHorizLeft, Help: ""},
	{Keys: []string{"L", "shift+right"}, Context: keyCtxChat, Action: actChatHorizRight, Help: ""},
	{Keys: []string{"l", "right"}, Context: keyCtxChat, Action: actFocusSidebar, Help: ""},
	{Keys: []string{"tab"}, Context: keyCtxChat, Action: actFocusEditor, Help: "tab editor"},

	// Sidebar (focus=sidebar): four-line steps, top/bottom, back to chat/editor.
	{Keys: []string{"j", "down", "ctrl+j"}, Context: keyCtxSidebar, Action: actSidebarDown, Help: "↑↓ scroll"},
	{Keys: []string{"k", "up", "ctrl+k"}, Context: keyCtxSidebar, Action: actSidebarUp, Help: ""},
	{Keys: []string{"g", "home"}, Context: keyCtxSidebar, Action: actSidebarTop, Help: ""},
	{Keys: []string{"G", "end"}, Context: keyCtxSidebar, Action: actSidebarBot, Help: ""},
	{Keys: []string{"h", "left"}, Context: keyCtxSidebar, Action: actFocusMain, Help: "h/← chat"},
	{Keys: []string{"tab"}, Context: keyCtxSidebar, Action: actFocusEditor, Help: ""},
}

// lookupBinding finds the binding for key in one context. The first match wins.
func lookupBinding(ctx keyContext, key string) (keyBinding, bool) {
	for _, b := range keyBindings {
		if b.Context != ctx {
			continue
		}
		for _, k := range b.Keys {
			if k == key {
				return b, true
			}
		}
	}
	return keyBinding{}, false
}

// lookupBindingIn finds the binding for key in the first context that has one,
// so modal contexts can fall through to global bindings (pgup/pgdown).
func lookupBindingIn(key string, contexts ...keyContext) (keyBinding, bool) {
	for _, ctx := range contexts {
		if b, ok := lookupBinding(ctx, key); ok {
			return b, true
		}
	}
	return keyBinding{}, false
}

// helpFor joins the help fragments for the given actions; actions with no
// fragment are skipped. Rendering the help line from this table is what keeps
// the hint text from drifting away from the dispatch above.
func helpFor(actions ...keyAction) string {
	var parts []string
	for _, act := range actions {
		for _, b := range keyBindings {
			if b.Action == act && b.Help != "" {
				parts = append(parts, b.Help)
				break
			}
		}
	}
	return joinHelpParts(parts)
}

func joinHelpParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}
