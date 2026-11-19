package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/agent"
)

// overlayKind identifies one modal surface. The stack keeps the routing and
// the render order in one place instead of a boolean per surface.
type overlayKind int

const (
	overlayApproval overlayKind = iota
	overlayPicker
	overlayPalette
)

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
	case "esc", "ctrl+k":
		m.closeCommandPalette()
		return *m, nil
	case "enter":
		return m.runPaletteSelection()
	case "up":
		m.palette.CursorUp()
		return *m, nil
	case "down":
		m.palette.CursorDown()
		return *m, nil
	}

	if !m.palette.SettingFilter() {
		if text, ok := printableInput(msg); ok {
			m.palette.SetFilterText(text)
			m.palette.SetFilterState(list.Filtering)
			return *m, nil
		}
		// Non-printable browsing keys (page scroll, quit) stay with the list.
		var cmd tea.Cmd
		m.palette, cmd = m.palette.Update(msg)
		return *m, cmd
	}

	var cmd tea.Cmd
	m.palette, cmd = m.palette.Update(msg)
	if m.palette.FilterValue() == "" {
		// Backspacing to an empty query must show every command again; fuzzy
		// matching an empty pattern would otherwise render an empty list.
		m.palette.ResetFilter()
		m.palette.SetItems(m.paletteItems)
		m.palette.Select(0)
	}
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
