package tui

import (
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

// NewModelPicker creates and configures the bubbles/list model picker.
func NewModelPicker(items []list.Item, width, height int) list.Model {
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

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(lipgloss.Color("#58a6ff")).Bold(true)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(lipgloss.Color("#8b949e"))

	l := list.New(items, delegate, w, h)
	l.Title = "Select Model (Fuzzy Search)"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.Styles.Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).MarginLeft(1)

	return l
}
