package tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// ModelItem represents a selectable model item in the fuzzy picker.
type ModelItem struct {
	Provider string
	Model    string
}

func (i ModelItem) Title() string       { return i.Model }
func (i ModelItem) Description() string { return "Provider: " + i.Provider }
func (i ModelItem) FilterValue() string { return i.Provider + " " + i.Model }

// Picker delegate geometry. The left padding is repeated by every delegate
// style because the list measures its text column from NormalTitle's padding;
// unselected rows are indented by that padding and the selected row by its
// border, so the two must stay in lockstep or the cursor jumps between lines.
const (
	// pickerRowPad is the indent of unselected row text.
	pickerRowPad = 2
	// pickerSelectedPad is one narrower than pickerRowPad, because the selected
	// row spends that cell on its border rule. These two must sum to the same
	// total or the cursor makes the text jump sideways on every row change.
	pickerSelectedPad = pickerRowPad - 1
	// pickerFramePad aligns the title, status and help rows with row text.
	pickerFramePad = 2
)

var (
	// Unselected rows are body text, so they recede and the cursor carries the
	// eye.
	pickerItemTitleStyle = lipgloss.NewStyle().
				Foreground(colorText).
				Padding(0, 0, 0, pickerRowPad)
	pickerItemDescStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg).
				Padding(0, 0, 0, pickerRowPad)

	// The cursor is marked by a left rule rather than a background wash: a
	// filled row would tint the text and fight the fuzzy-match highlighting
	// layered on top of it.
	pickerSelectedTitleStyle = lipgloss.NewStyle().
					Border(lipgloss.NormalBorder(), false, false, false, true).
					BorderForeground(colorAccent).
					Foreground(colorAccent).
					Bold(true).
					Padding(0, 0, 0, pickerSelectedPad)
	pickerSelectedDescStyle = pickerSelectedTitleStyle.
				Bold(false).
				Foreground(colorMutedFg)

	// Filter matches are the highlight colour rather than the cursor's accent,
	// so "where did it match" stays legible even on the selected row.
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

// pickerDelegate builds the row renderer. Left borders, not fills or colour
// swaps alone, signal selection so the row's fuzzy-match highlighting survives.
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

// NewModelPicker creates and configures the bubbles/list model picker.
//
// The picker is framed by modalOverlayStyle by its caller, so it draws no
// border of its own and stays quiet until the cursor moves onto a row.
func NewModelPicker(items []list.Item, width, height int) list.Model {
	// The caller re-sizes the picker on every window resize, so these clamps are
	// only the initial bounds: wide enough for a provider name plus model id,
	// short enough that a long model list still fits beside the modal padding.
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

	// Naming the item type turns the status bar from "2 items" into "2 models",
	// and makes the empty state read "No models." instead of "No items.".
	l.SetStatusBarItemName("model", "models")

	// The placeholder is what a user sees when they open the filter with nothing
	// typed; without it the prompt sits empty and looks broken.
	l.FilterInput.Placeholder = "type to filter by provider or model"
	l.FilterInput.PlaceholderStyle = pickerPlaceholderStyle

	l.Styles.Title = pickerTitleStyle
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, pickerFramePad)
	l.Styles.FilterPrompt = pickerFilterPromptStyle
	l.Styles.NoItems = pickerNoItemsStyle
	l.Styles.StatusBar = lipgloss.NewStyle().Foreground(colorMutedFg).Padding(0, 0, 1, pickerFramePad)
	l.Styles.StatusEmpty = pickerNoItemsStyle
	l.Styles.StatusBarFilterCount = pickerNoItemsStyle
	l.Styles.HelpStyle = lipgloss.NewStyle().Padding(1, 0, 0, pickerFramePad)

	l.Help.Styles.ShortKey = pickerHelpStyle.Bold(true)
	l.Help.Styles.ShortDesc = pickerHelpStyle
	l.Help.Styles.FullKey = pickerHelpStyle.Bold(true)
	l.Help.Styles.FullDesc = pickerHelpStyle

	// Surface the select action, since the list's own help omits it and the
	// picker is dismissed the moment Enter is pressed.
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		}
	}

	return l
}
