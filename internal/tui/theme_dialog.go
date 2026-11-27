package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/config"
)

// Theme dialogs: a picker that previews live while
// the cursor moves, reverts on esc and persists on enter; and a palette
// editor whose save writes the theme file BEFORE touching the UI, so a
// failed write never leaves colours applied that didn't reach disk.

type themeItem struct {
	name    string
	builtin bool
}

func (t themeItem) Title() string { return t.name }
func (t themeItem) Description() string {
	if t.builtin {
		return "built-in"
	}
	return "user"
}
func (t themeItem) FilterValue() string { return t.name }

// themeListItems lists built-ins first (sorted), then user themes.
func themeListItems() []list.Item {
	names := make([]string, 0, len(builtinThemesByKey))
	for k := range builtinThemesByKey {
		names = append(names, k)
	}
	sort.Strings(names)
	items := make([]list.Item, 0, len(names))
	for _, n := range names {
		items = append(items, themeItem{name: n, builtin: true})
	}
	users, _ := loadUserThemes()
	for _, u := range users {
		items = append(items, themeItem{name: u.Name})
	}
	return items
}

// newThemePickerList builds the theme list with the shared picker mechanics.
// It is also used at NewModel time so resize never touches a zero-value list.
func newThemePickerList(items []list.Item, width, height int) list.Model {
	w, h := paletteSize(width, height)
	l := list.New(items, pickerDelegate(), w, h)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()
	l.Styles.Title = pickerTitleStyle
	l.Styles.NoItems = pickerNoItemsStyle
	return l
}

func (m *Model) openThemePicker() {
	m.clearCompletion()
	m.themeBaseName = activeTheme.Name
	m.themeItems = themeListItems()
	m.themePicker = newThemePickerList(m.themeItems, m.width, m.height)
	for i, item := range m.themeItems {
		if ti, ok := item.(themeItem); ok && ti.name == activeTheme.Name {
			m.themePicker.Select(i)
			break
		}
	}
	m.overlays.push(overlayTheme)
}

func (m *Model) resizeThemePicker() {
	w, h := paletteSize(m.width, m.height)
	m.themePicker.SetWidth(w)
	m.themePicker.SetHeight(h)
}

func (m *Model) closeThemePicker() {
	m.overlays.pop(overlayTheme)
}

func (m Model) updateThemePicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Revert to the theme that was active when the picker opened.
		_ = m.ApplyTheme(m.themeBaseName)
		m.closeThemePicker()
		return m, nil
	case "enter":
		var cmd tea.Cmd
		if sel, ok := m.themePicker.SelectedItem().(themeItem); ok {
			if err := persistActiveTheme(sel.name); err != nil {
				m.addMessage(ChatMessage{Role: RoleError, Label: "GoCode", Content: "Theme not saved: " + err.Error()})
				cmd = m.showToast(toastError, "Theme not saved: "+err.Error())
			} else {
				cmd = m.showToast(toastSuccess, "Theme saved: "+sel.name)
			}
		}
		m.closeThemePicker()
		return m, cmd
	case "ctrl+e":
		if sel, ok := m.themePicker.SelectedItem().(themeItem); ok {
			m.openThemeEditor(sel)
		}
		return m, nil
	case "up":
		m.themePicker.CursorUp()
		m.previewThemeSelection()
		return m, nil
	case "down":
		m.themePicker.CursorDown()
		m.previewThemeSelection()
		return m, nil
	}
	var cmd tea.Cmd
	m.themePicker, cmd = filterListUpdate(m.themePicker, m.themeItems, msg)
	return m, cmd
}

func (m *Model) previewThemeSelection() {
	if sel, ok := m.themePicker.SelectedItem().(themeItem); ok {
		_ = m.ApplyTheme(sel.name)
	}
}

// persistActiveTheme writes the choice to config so it survives restarts.
func persistActiveTheme(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.TUI.ActiveTheme = name
	return config.Save(cfg)
}

func (m *Model) renderThemePicker() string {
	width := max(34, min(70, m.width-2))
	body := m.themePicker.View() + "\n" + dialogHelp(
		[2]string{"↑↓", "preview"}, [2]string{"enter", "save"}, [2]string{"esc", "revert"}, [2]string{"ctrl+e", "edit"},
	)
	return dialogFrame("Themes", dialogNormal, body, width)
}

// --- palette editor -------------------------------------------------------

// themeEditor edits one token at a time over the canonical token order. The
// preview is derived from the base theme plus the edited values; esc restores
// the theme that was active when the editor opened.
type themeEditor struct {
	name     string
	base     string
	keys     []string
	values   map[string]string
	cursor   int
	input    textinput.Model
	revertTo string
	errMsg   string
}

func (m *Model) openThemeEditor(item themeItem) {
	baseTheme, err := ResolveTheme(item.name)
	if err != nil {
		return
	}
	name := item.name
	if item.builtin {
		// Editing a built-in saves a user override, never the built-in itself.
		name = item.name + "-custom"
	}
	keys := make([]string, 0, len(tokenSpecs))
	values := make(map[string]string, len(tokenSpecs))
	for _, s := range tokenSpecs {
		keys = append(keys, s.name)
		values[s.name] = hexOf(s.get(baseTheme.Tokens))
	}
	ti := textinput.New()
	ti.CharLimit = 32
	ti.SetValue(values[keys[0]])
	ti.Focus()
	m.editor = themeEditor{
		name:     name,
		base:     item.name,
		keys:     keys,
		values:   values,
		input:    ti,
		revertTo: activeTheme.Name,
	}
	m.overlays.push(overlayThemeEditor)
}

// commitEditorInput parses the current field; a valid colour updates the
// value map and the live preview, an invalid one keeps the old value and
// surfaces the reason.
func (m *Model) commitEditorInput() {
	v := strings.TrimSpace(m.editor.input.Value())
	if v == "" {
		return
	}
	if _, err := parseColorSpec(v); err != nil {
		m.editor.errMsg = "invalid colour: " + v
		return
	}
	m.editor.errMsg = ""
	m.editor.values[m.editor.keys[m.editor.cursor]] = v
	m.previewEditor()
}

func (m *Model) previewEditor() {
	baseTheme, err := ResolveTheme(m.editor.base)
	if err != nil {
		return
	}
	th, err := deriveUserTheme(baseTheme, m.editor.name, m.editor.values)
	if err != nil {
		return
	}
	activateTheme(th)
	m.refreshThemeView(th)
}

func (m Model) updateThemeEditor(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		_ = m.ApplyTheme(m.editor.revertTo)
		m.overlays.pop(overlayThemeEditor)
		return m, nil
	case "enter", "ctrl+s":
		m.commitEditorInput()
		if err := m.saveEditorTheme(); err != nil {
			m.editor.errMsg = err.Error()
			return m, nil
		}
		// The file is on disk; only now commit the UI to the saved theme.
		_ = m.ApplyTheme(m.editor.name)
		m.themeItems = themeListItems()
		m.themePicker.SetItems(m.themeItems)
		m.overlays.pop(overlayThemeEditor)
		return m, m.showToast(toastSuccess, "Theme saved: "+m.editor.name)
	case "tab", "shift+tab":
		m.commitEditorInput()
		n := len(m.editor.keys)
		if msg.String() == "tab" {
			m.editor.cursor = (m.editor.cursor + 1) % n
		} else {
			m.editor.cursor = (m.editor.cursor - 1 + n) % n
		}
		m.editor.input.SetValue(m.editor.values[m.editor.keys[m.editor.cursor]])
		m.editor.input.CursorEnd()
		return m, nil
	}
	var cmd tea.Cmd
	m.editor.input, cmd = m.editor.input.Update(msg)
	m.commitEditorInput()
	return m, cmd
}

// saveEditorTheme writes only the tokens that differ from the base, so the
// user file stays a small, readable override.
func (m *Model) saveEditorTheme() error {
	baseTheme, err := ResolveTheme(m.editor.base)
	if err != nil {
		return err
	}
	overrides := make(map[string]string)
	for _, s := range tokenSpecs {
		want := m.editor.values[s.name]
		if have := hexOf(s.get(baseTheme.Tokens)); !strings.EqualFold(want, have) {
			overrides[s.name] = want
		}
	}
	return saveUserThemeFile(m.editor.name, m.editor.base, overrides)
}

func (m *Model) renderThemeEditor() string {
	width := max(40, min(70, m.width-2))
	e := m.editor
	var b strings.Builder
	// A sliding window of fields keeps the dialog inside 20 rows.
	const window = 10
	start := max(0, min(e.cursor-window/2, len(e.keys)-window))
	end := min(len(e.keys), start+window)
	for i := start; i < end; i++ {
		line := fmt.Sprintf("%-18s %s", e.keys[i], e.values[e.keys[i]])
		if i == e.cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(activeTheme.Tokens.Primary).Bold(true).Render("▸ " + line))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(activeTheme.Tokens.FgSubtle).Render("  " + line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n> " + e.input.View())
	if e.errMsg != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(activeTheme.Tokens.Destructive).Render(e.errMsg))
	}
	b.WriteString("\n" + dialogHelp(
		[2]string{"tab/shift+tab", "field"}, [2]string{"enter/ctrl+s", "save"}, [2]string{"esc", "revert"},
	))
	return dialogFrame("Edit theme: "+e.name, dialogNormal, b.String(), width)
}
