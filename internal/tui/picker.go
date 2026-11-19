package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ModelItem is a selectable model in the fuzzy picker.
type ModelItem struct {
	Provider string
	Model    string
}

func (i ModelItem) Title() string       { return i.Model }
func (i ModelItem) Description() string { return "Provider: " + i.Provider }
func (i ModelItem) FilterValue() string { return i.Provider + " " + i.Model }

// SessionItem is one resumable session in the session picker. The full ID is
// what /resume receives; the row shows the generated suffix plus the same
// fields the text listing prints (title, updated time, message count), so the
// picker and `/sessions` describe a session the same way. The stored title is
// an excerpt of the first user message; Excerpt avoids colliding with the
// list.Item Title() method.
type SessionItem struct {
	ID        string
	Excerpt   string
	Provider  string
	Model     string
	UpdatedAt time.Time
	Messages  int
}

func (s SessionItem) Title() string {
	excerpt := strings.TrimSpace(s.Excerpt)
	if excerpt == "" {
		excerpt = "(untitled)"
	}
	return sessionIDShort(s.ID) + "  " + truncateToWidth(excerpt, sessionTitleExcerpt)
}

func (s SessionItem) Description() string {
	parts := []string{
		s.UpdatedAt.Local().Format("Jan 02 15:04"),
		fmt.Sprintf("%d msgs", s.Messages),
	}
	if name := strings.Trim(strings.TrimSpace(s.Provider)+"/"+strings.TrimSpace(s.Model), "/"); name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, " · ")
}

// FilterValue includes the whole ID so a user who knows it can paste it into
// the filter instead of scrolling.
func (s SessionItem) FilterValue() string {
	return s.ID + " " + s.Excerpt + " " + s.Provider + " " + s.Model
}

// sessionTitleExcerpt is the row budget for the title; the delegate wraps
// anything longer into a second line and pushes the list out of shape.
const sessionTitleExcerpt = 44

// sessionIDShort is the identity a row shows. Generated IDs end in a random
// suffix (sess_<timestamp>_<hex>); that suffix is the part a human compares.
// Ids that do not have that shape are shown whole.
func sessionIDShort(id string) string {
	if i := strings.LastIndex(id, "_"); i >= 0 && len(id)-i-1 >= 6 {
		return id[i+1:]
	}
	return id
}

// NormalTitle padding and selected-row border must stay in lockstep or cursor jumps.
const (
	pickerRowPad = 2
	// One narrower: selected row spends a cell on border.
	pickerSelectedPad = pickerRowPad - 1
	// Aligns title, status and help rows with row text.
	pickerFramePad = 2
)

var (
	// Body text so cursor carries the eye.
	pickerItemTitleStyle = lipgloss.NewStyle().
				Foreground(colorText).
				Padding(0, 0, 0, pickerRowPad)
	pickerItemDescStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg).
				Padding(0, 0, 0, pickerRowPad)

	// Left rule, not fill: fill would fight fuzzy-match highlighting.
	pickerSelectedTitleStyle = lipgloss.NewStyle().
					Border(lipgloss.NormalBorder(), false, false, false, true).
					BorderForeground(colorAccent).
					Foreground(colorAccent).
					Bold(true).
					Padding(0, 0, 0, pickerSelectedPad)
	pickerSelectedDescStyle = pickerSelectedTitleStyle.
				Bold(false).
				Foreground(colorMutedFg)

	// Highlight hue, not accent, so matches stay legible on selected row.
	pickerFilterMatchStyle = lipgloss.NewStyle().
				Foreground(colorHighlight).
				Underline(true)

	pickerDimmedStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg).
				Padding(0, 0, 0, pickerRowPad)

	pickerTitleStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	pickerNoItemsStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg).
				Italic(true)

	pickerFilterPromptStyle = lipgloss.NewStyle().
				Foreground(colorAccent)

	pickerPlaceholderStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg)

	pickerHelpStyle = lipgloss.NewStyle().
			Foreground(colorMutedFg)
)

func pickerDelegate() list.DefaultDelegate {
	delegate := list.NewDefaultDelegate()

	delegate.Styles.NormalTitle = pickerItemTitleStyle
	delegate.Styles.NormalDesc = pickerItemDescStyle
	delegate.Styles.SelectedTitle = pickerSelectedTitleStyle
	delegate.Styles.SelectedDesc = pickerSelectedDescStyle
	delegate.Styles.FilterMatch = pickerFilterMatchStyle
	delegate.Styles.DimmedTitle = pickerDimmedStyle
	delegate.Styles.DimmedDesc = pickerDimmedStyle

	return delegate
}

// newFuzzyList builds the chrome both pickers share: one list with a title, a
// filter prompt, the picker palette and the action Enter performs surfaced in
// the help row. Sizing is the caller's job, because the overlays resize on
// every window-size message.
func newFuzzyList(items []list.Item, title, itemName, placeholder, selectVerb string, width, height int) list.Model {
	l := list.New(items, pickerDelegate(), width, height)
	l.Title = title
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetStatusBarItemName(itemName, itemName+"s")
	// Empty prompt looks broken without it.
	l.FilterInput.Placeholder = placeholder
	// A stray "q" must filter, not quit the program.
	l.DisableQuitKeybindings()

	// v2 Styles() returns a copy, so write back via SetStyles.
	filterStyles := l.FilterInput.Styles()
	filterStyles.Focused.Placeholder = pickerPlaceholderStyle
	filterStyles.Blurred.Placeholder = pickerPlaceholderStyle
	l.FilterInput.SetStyles(filterStyles)

	l.Styles.Title = pickerTitleStyle
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, pickerFramePad)
	// Same colour in both states so prompt doesn't shift on focus.
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

	// List help omits the action that actually runs, so surface it.
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", selectVerb)),
		}
	}

	return l
}

// pickerFrame is the overlay size for a picker: wide enough for a model id,
// shallow enough to leave the transcript visible above and below.
func pickerFrame(width, height int) (int, int) {
	return max(1, width-10), max(1, height-6)
}

// Caller frames with modalOverlayStyle, so no border here.
func NewModelPicker(items []list.Item, width, height int) list.Model {
	// Only initial bounds; caller re-sizes on every window resize.
	w, h := pickerFrame(width, height)
	if w > 60 {
		w = 60
	}
	if w < 30 {
		w = 30
	}
	if h > 16 {
		h = 16
	}
	if h < 8 {
		h = 8
	}
	return newFuzzyList(items, "Select Model", "model", "type to filter by provider or model", "select", w, h)
}

// NewSessionPicker builds the /sessions overlay. Same list mechanics as the
// model picker, so filtering and cursor keys behave identically.
func NewSessionPicker(items []list.Item, width, height int) list.Model {
	w, h := pickerFrame(width, height)
	if w > 76 {
		w = 76
	}
	if w < 30 {
		w = 30
	}
	if h > 16 {
		h = 16
	}
	if h < 8 {
		h = 8
	}
	return newFuzzyList(items, "Resume Session", "session", "type to filter sessions", "resume", w, h)
}

// --- refresh on open ------------------------------------------------------

// pickerRefreshMsg carries one background collection back to the UI. An empty
// item slice means every provider failed or timed out; the snapshot already on
// screen is then left alone.
type pickerRefreshMsg struct {
	items []list.Item
}

// refreshModelPicker re-collects the model list off the update loop, bounded by
// the same per-provider/overall deadlines as startup. The startup snapshot
// stays on screen until the refresh lands, so a slow or failed collection can
// never empty the picker or block the UI. A collection already in flight is not
// duplicated.
func (m *Model) refreshModelPicker() tea.Cmd {
	if m.pickerRefreshInFlight || m.registry == nil {
		return nil
	}
	m.pickerRefreshInFlight = true
	reg := m.registry
	return func() tea.Msg {
		return pickerRefreshMsg{items: collectPickerItems(context.Background(), reg)}
	}
}

// applyPickerRefresh installs a refreshed collection, keeping the highlighted
// model selected when it still exists. With a filter typed the visible rows are
// recomputed asynchronously, so the identity is parked on the model and applied
// when the match message lands.
func (m *Model) applyPickerRefresh(items []list.Item) tea.Cmd {
	if len(items) == 0 {
		// A failed or timed-out collection is not a reason to lose the list.
		return nil
	}
	prev, hadPrev := m.modelPicker.SelectedItem().(ModelItem)
	filtered := m.modelPicker.FilterValue() != ""
	cmd := m.modelPicker.SetItems(items)
	if !hadPrev {
		m.pickerKeep = nil
		m.modelPicker.Select(0)
		return cmd
	}
	if filtered {
		m.pickerKeep = &prev
		return cmd
	}
	m.pickerKeep = nil
	m.selectModelItem(prev)
	return cmd
}

// selectModelItem moves the cursor to want when it is in the visible rows. The
// cursor is reset only when the identity is gone, so a refresh that still has
// the user's model never moves it.
func (m *Model) selectModelItem(want ModelItem) bool {
	for i, item := range m.modelPicker.VisibleItems() {
		if mi, ok := item.(ModelItem); ok && mi == want {
			m.modelPicker.Select(i)
			return true
		}
	}
	m.modelPicker.Select(0)
	return false
}
