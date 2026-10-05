package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
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

// Caller frames with modalOverlayStyle, so no border here.
func NewModelPicker(items []list.Item, width, height int) list.Model {
	// Only initial bounds; caller re-sizes on every window resize.
	w := width - 10
	if w > 60 {
		w = 60
	}
	if w < 30 {
		w = 30
	}
	h := height - 6
	if h > 16 {
		h = 16
	}
	if h < 8 {
		h = 8
	}

	l := list.New(items, pickerDelegate(), w, h)
	l.Title = "Select Model"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)

	l.SetStatusBarItemName("model", "models")

	// Empty prompt looks broken without it.
	l.FilterInput.Placeholder = "type to filter by provider or model"

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

	// List help omits select action, so surface it.
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		}
	}

	return l
}
