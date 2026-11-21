package tui

import (
	"errors"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/safetext"
	"github.com/mevarx/GoCode/internal/session"
)

// overlayKind identifies one modal surface. The stack keeps the routing and
// the render order in one place instead of a boolean per surface.
type overlayKind int

const (
	overlayApproval overlayKind = iota
	overlayPicker
	overlayPalette
	overlaySession
	overlayQuit
	overlayTheme
	overlayThemeEditor
)

// overlayTitle names a surface for tests and diagnostics.
func (k overlayKind) String() string {
	switch k {
	case overlayApproval:
		return "approval"
	case overlayPicker:
		return "picker"
	case overlayPalette:
		return "palette"
	case overlaySession:
		return "session"
	case overlayQuit:
		return "quit"
	case overlayTheme:
		return "theme"
	case overlayThemeEditor:
		return "theme-editor"
	}
	return "unknown"
}

// overlayStack is the focus order for modal surfaces. Approval always wins:
// pushing it drops everything else, so a tool request that arrives while the
// palette or picker is open can never be hidden behind it.
type overlayStack struct {
	kinds []overlayKind
}

func (s *overlayStack) push(kind overlayKind) {
	if kind == overlayApproval {
		s.kinds = append(s.kinds[:0], overlayApproval)
		return
	}
	if s.has(overlayApproval) {
		// Approval owns focus; a non-approval overlay cannot open over it.
		return
	}
	if s.has(kind) {
		return
	}
	s.kinds = append(s.kinds, kind)
}

func (s *overlayStack) pop(kind overlayKind) {
	for i, k := range s.kinds {
		if k == kind {
			s.kinds = append(s.kinds[:i], s.kinds[i+1:]...)
			return
		}
	}
}

func (s *overlayStack) has(kind overlayKind) bool {
	for _, k := range s.kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func (s *overlayStack) empty() bool { return len(s.kinds) == 0 }

// top returns the focused overlay; ok is false when no overlay is open.
func (s *overlayStack) top() (overlayKind, bool) {
	if len(s.kinds) == 0 {
		return 0, false
	}
	return s.kinds[len(s.kinds)-1], true
}

// commandItem is one palette row backed by the agent's command registry.
type commandItem struct {
	Name  string
	Desc  string
	Usage string
}

func (c commandItem) Title() string       { return c.Name }
func (c commandItem) Description() string { return c.Desc }
func (c commandItem) FilterValue() string { return c.Name + " " + c.Desc }

// commandPaletteItems projects the command registry into palette rows.
func commandPaletteItems(specs []agent.CommandSpec) []list.Item {
	items := make([]list.Item, 0, len(specs))
	for _, spec := range specs {
		items = append(items, commandItem{
			Name:  spec.Name,
			Desc:  spec.Description,
			Usage: spec.Usage,
		})
	}
	return items
}

// NewCommandPalette builds the Ctrl+K overlay: a fuzzy list over the same
// registry dispatch and /help read.
func NewCommandPalette(specs []agent.CommandSpec, width, height int) list.Model {
	items := commandPaletteItems(specs)

	w, h := paletteSize(width, height)
	l := list.New(items, pickerDelegate(), w, h)
	l.Title = "Commands"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetStatusBarItemName("command", "commands")
	l.FilterInput.Placeholder = "type to filter commands"
	// A stray "q" must filter, not quit the program.
	l.DisableQuitKeybindings()

	filterStyles := l.FilterInput.Styles()
	filterStyles.Focused.Placeholder = pickerPlaceholderStyle
	filterStyles.Blurred.Placeholder = pickerPlaceholderStyle
	l.FilterInput.SetStyles(filterStyles)

	l.Styles.Title = pickerTitleStyle
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, pickerFramePad)
	l.Styles.Filter.Focused.Prompt = pickerFilterPromptStyle
	l.Styles.Filter.Blurred.Prompt = pickerFilterPromptStyle
	l.Styles.NoItems = pickerNoItemsStyle
	l.Styles.StatusBar = lipgloss.NewStyle().Foreground(colorMutedFg).Padding(0, 0, 1, pickerFramePad)
	l.Styles.StatusEmpty = pickerNoItemsStyle
	l.Styles.StatusBarFilterCount = pickerNoItemsStyle
	l.Styles.HelpStyle = lipgloss.NewStyle().Padding(1, 0, 0, pickerFramePad)
	l.Help.Styles.ShortKey = pickerHelpStyle.Bold(true)
	l.Help.Styles.ShortDesc = pickerHelpStyle
	l.Help.Styles.FullKey = pickerHelpStyle.Bold(true)
	l.Help.Styles.FullDesc = pickerHelpStyle

	// The list's own help omits selection; surface the action that runs.
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run")),
		}
	}

	return l
}

func paletteSize(width, height int) (int, int) {
	w := width - 14
	if w > 64 {
		w = 64
	}
	if w < 34 {
		w = 34
	}
	h := height - 8
	if h > 16 {
		h = 16
	}
	if h < 8 {
		h = 8
	}
	return w, h
}

// commandTakesArgs reports whether a command's usage advertises arguments.
// Palette selections for those stage the command at the prompt so the user can
// type the argument; argument-free commands run immediately.
func commandTakesArgs(usage string) bool {
	return strings.ContainsAny(usage, "[<")
}

// openCommandPalette resets the list to its full registry and puts focus on it.
// Filtering starts on the first printable key (see updatePalette): bubbles has
// no public "start filtering" call, and seeding the filter avoids synthesising
// the list's own "/" keystroke.
func (m *Model) openCommandPalette() {
	m.clearCompletion()
	m.overlays.push(overlayPalette)
	m.palette.ResetFilter()
	m.palette.SetItems(m.paletteItems)
	m.palette.Select(0)
	m.palette.FilterInput.Reset()
	m.resizePalette()
}

func (m *Model) resizePalette() {
	w, h := paletteSize(m.width, m.height)
	m.palette.SetWidth(w)
	m.palette.SetHeight(h)
}

func (m *Model) closeCommandPalette() {
	m.overlays.pop(overlayPalette)
	m.palette.ResetFilter()
	m.palette.SetItems(m.paletteItems)
	m.palette.Select(0)
}

// updatePalette routes keys to the palette. Enter runs the selected command,
// Esc/Ctrl+K close, Up/Down move the cursor, and printable input seeds or
// extends the fuzzy filter.
func (m *Model) updatePalette(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+k", "ctrl+p":
		m.closeCommandPalette()
		return *m, nil
	case "enter":
		return m.runPaletteSelection()
	case "up":
		m.palette.CursorUp()
		return *m, nil
	case "down", "ctrl+n":
		m.palette.CursorDown()
		return *m, nil
	}

	var cmd tea.Cmd
	m.palette, cmd = filterListUpdate(m.palette, m.paletteItems, msg)
	return *m, cmd
}

// filterListUpdate applies one key to a fuzzy list: the first printable key
// starts the filter (bubbles has no public "start filtering" call, and seeding
// the filter avoids synthesising the list's own "/" keystroke), later keys go
// to the filter input, and backspacing to empty restores every item because
// fuzzy-matching an empty pattern would otherwise render an empty list.
func filterListUpdate(l list.Model, items []list.Item, msg tea.KeyPressMsg) (list.Model, tea.Cmd) {
	if !l.SettingFilter() {
		if text, ok := printableInput(msg); ok {
			l.SetFilterText(text)
			l.SetFilterState(list.Filtering)
			return l, nil
		}
		// Non-printable browsing keys (page scroll, quit) stay with the list.
		return l.Update(msg)
	}

	l, cmd := l.Update(msg)
	if l.FilterValue() == "" {
		l.ResetFilter()
		l.SetItems(items)
		l.Select(0)
	}
	return l, cmd
}

// --- session picker -------------------------------------------------------

// sessionListLimit mirrors the depth of the text listing in
// agent.handleSessions (store.ListSessions(20)), so the picker never shows a
// session the engine's own command would not.
const sessionListLimit = 20

// sessionListMsg carries one asynchronous listing into the picker. err is set
// when the store is missing or the query failed; the overlay then falls back
// to a transcript notice instead of opening an empty list.
type sessionListMsg struct {
	summaries []session.SessionSummary
	err       error
}

// sessionItemsFromSummaries projects the store's listing into picker rows.
func sessionItemsFromSummaries(summaries []session.SessionSummary) []list.Item {
	items := make([]list.Item, 0, len(summaries))
	for _, sum := range summaries {
		items = append(items, SessionItem{
			ID:        sum.ID,
			Excerpt:   sum.Title,
			Provider:  sum.Provider,
			Model:     sum.Model,
			UpdatedAt: sum.UpdatedAt,
			Messages:  sum.MessageCount,
		})
	}
	return items
}

// openSessionPicker shows the overlay immediately (the list fills in when the
// listing lands) and returns the command that performs the listing off the
// update loop.
func (m *Model) openSessionPicker() tea.Cmd {
	m.clearCompletion()
	m.overlays.push(overlaySession)
	m.sessionItems = nil
	m.sessionPicker.ResetFilter()
	m.sessionPicker.SetItems(nil)
	m.resizeSessionPicker()
	return m.loadSessions()
}

func (m *Model) resizeSessionPicker() {
	w, h := pickerFrame(m.width, m.height)
	m.sessionPicker.SetWidth(w)
	m.sessionPicker.SetHeight(h)
}

func (m *Model) closeSessionPicker() {
	m.overlays.pop(overlaySession)
	m.sessionMode = sessionNormal
	m.syncSessionPickerTitle()
	m.sessionPicker.ResetFilter()
	m.sessionPicker.SetItems(nil)
	m.sessionPicker.Select(0)
}

// loadSessions reads the listing through the same store method /sessions uses.
// The store does synchronous SQLite work, so it runs in the command goroutine;
// a nil session or store reports through the message instead of blocking.
func (m *Model) loadSessions() tea.Cmd {
	sess := m.agentSession
	if sess == nil || sess.Store() == nil {
		return func() tea.Msg {
			return sessionListMsg{err: errSessionStoreUnavailable}
		}
	}
	store := sess.Store()
	return func() tea.Msg {
		summaries, err := store.ListSessions(sessionListLimit)
		return sessionListMsg{summaries: summaries, err: err}
	}
}

// errSessionStoreUnavailable is surfaced in the transcript when the picker has
// no store to read; it matches the engine's own wording.
var errSessionStoreUnavailable = errors.New("session store is not enabled")

// applySessionList installs a listing, or falls back to a transcript notice
// when the store is missing, the query failed, or nothing is saved. A closed
// picker drops the result: the user already moved on.
func (m *Model) applySessionList(msg sessionListMsg) tea.Cmd {
	if msg.err != nil {
		if m.overlays.has(overlaySession) {
			m.closeSessionPicker()
			content := "Session listing failed: " + safetext.Sanitize(msg.err.Error())
			if errors.Is(msg.err, errSessionStoreUnavailable) {
				// Same wording as the engine's text command, so the two paths
				// never describe a missing store differently.
				content = "Session store is not enabled."
			}
			m.addMessage(ChatMessage{Role: RoleSystem, Label: "GoCode", Content: content})
			m.viewport.GotoBottom()
		}
		return nil
	}
	m.sessionItems = sessionItemsFromSummaries(msg.summaries)
	if !m.overlays.has(overlaySession) {
		return nil
	}
	if len(m.sessionItems) == 0 {
		m.closeSessionPicker()
		m.addMessage(ChatMessage{Role: RoleSystem, Label: "GoCode", Content: "No saved sessions found."})
		m.viewport.GotoBottom()
		return nil
	}
	cmd := m.sessionPicker.SetItems(m.sessionItems)
	m.sessionPicker.Select(0)
	return cmd
}

// updateSessionPicker routes keys through the three-state machine: normal
// browsing/resume, deleting (ctrl+x → y/enter/n/esc) and renaming (ctrl+r →
// enter/esc with an inline input). Esc closes from normal mode; Enter resumes
// the highlighted session by handing "/resume <id>" to the engine (which owns
// validation), and printable input filters like the palette.
func (m *Model) updateSessionPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.String()

	switch m.sessionMode {
	case sessionDeleting:
		switch key {
		case "y", "enter":
			m.sessionMode = sessionNormal
			m.syncSessionPickerTitle()
			if sel, ok := m.sessionPicker.SelectedItem().(SessionItem); ok {
				return *m, m.sessionStoreCmd(func(st *session.SessionStore) error {
					return st.DeleteSession(sel.ID)
				})
			}
		case "n", "esc":
			m.sessionMode = sessionNormal
			m.syncSessionPickerTitle()
		}
		return *m, nil

	case sessionRenaming:
		switch key {
		case "enter":
			title := strings.TrimSpace(m.renameInput.Value())
			m.sessionMode = sessionNormal
			m.syncSessionPickerTitle()
			if sel, ok := m.sessionPicker.SelectedItem().(SessionItem); ok && title != "" {
				return *m, m.sessionStoreCmd(func(st *session.SessionStore) error {
					return st.UpdateTitle(sel.ID, title)
				})
			}
			return *m, nil
		case "esc":
			m.sessionMode = sessionNormal
			m.syncSessionPickerTitle()
			return *m, nil
		}
		var cmd tea.Cmd
		m.renameInput, cmd = m.renameInput.Update(msg)
		return *m, cmd
	}

	switch key {
	case "esc":
		m.closeSessionPicker()
		return *m, nil
	case "enter":
		if !m.sessionPicker.SettingFilter() {
			if sel, ok := m.sessionPicker.SelectedItem().(SessionItem); ok {
				m.closeSessionPicker()
				// Same engine path as typing the command: store lookup,
				// provider switch and transcript load all stay in one place.
				if inputCh := m.inputCh; inputCh != nil {
					go func() { inputCh <- "/resume " + sel.ID }()
				}
				return *m, m.listenOutput()
			}
			return *m, nil
		}
	case "ctrl+x":
		if sel, ok := m.sessionPicker.SelectedItem().(SessionItem); ok {
			if m.agentSession != nil && sel.ID == m.agentSession.ID() && m.streaming {
				m.addMessage(ChatMessage{Role: RoleSystem, Label: "GoCode", Content: "Agent is busy, please wait..."})
				return *m, nil
			}
			m.sessionMode = sessionDeleting
			m.syncSessionPickerTitle()
		}
		return *m, nil
	case "ctrl+r":
		if sel, ok := m.sessionPicker.SelectedItem().(SessionItem); ok {
			ti := textinput.New()
			ti.CharLimit = 120
			ti.SetValue(sel.Excerpt)
			ti.CursorEnd()
			ti.Focus()
			m.renameInput = ti
			m.sessionMode = sessionRenaming
			m.syncSessionPickerTitle()
		}
		return *m, nil
	case "up":
		m.sessionPicker.CursorUp()
		return *m, nil
	case "down":
		m.sessionPicker.CursorDown()
		return *m, nil
	}

	var cmd tea.Cmd
	m.sessionPicker, cmd = filterListUpdate(m.sessionPicker, m.sessionItems, msg)
	return *m, cmd
}

// printableInput reports the text of a printable key press (no modifiers).
func printableInput(msg tea.KeyPressMsg) (string, bool) {
	if msg.Text == "" || msg.Mod != 0 {
		return "", false
	}
	return msg.Text, true
}

// runPaletteSelection executes the highlighted command. Argument-free commands
// go straight through the input channel, exactly like typing them; commands
// that take arguments are staged at the prompt so the user can type them.
func (m *Model) runPaletteSelection() (Model, tea.Cmd) {
	sel := m.palette.SelectedItem()
	item, ok := sel.(commandItem)
	if !ok {
		m.closeCommandPalette()
		return *m, nil
	}
	m.closeCommandPalette()

	// /sessions takes an optional id, but staging the prompt would only ask the
	// user to re-submit what the picker already offers.
	if item.Name == "/sessions" {
		m.clearCompletion()
		return *m, m.openSessionPicker()
	}

	if commandTakesArgs(item.Usage) {
		m.clearCompletion()
		m.textarea.SetValue(item.Name + " ")
		m.textarea.CursorEnd()
		focus := m.textarea.Focus()
		m.reflow()
		return *m, focus
	}

	if inputCh := m.inputCh; inputCh != nil {
		go func() { inputCh <- item.Name }()
	}
	return *m, m.listenOutput()
}
