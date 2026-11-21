package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Dialog chrome: rounded primary frame,
// gradient ╱ title rule, undimmed backdrop (placeModal already composites over
// the live chat), borderless buttons with a mnemonic underline, and badges
// whose ink obeys the contrast rule. Deleting/renaming variants recolour
// the frame and title instead of adding chrome.

type dialogVariant int

const (
	dialogNormal dialogVariant = iota
	dialogDeleting
	dialogRenaming
)

func (v dialogVariant) borderColor() color.Color {
	t := activeTheme.Tokens
	switch v {
	case dialogDeleting:
		return t.Destructive
	case dialogRenaming:
		return t.WarningSubtle
	}
	return t.Primary
}

// gradientRule fills width cells with ╱ blending from→to, the spec's title
// rule. Non-positive widths render nothing rather than a stray cell.
func gradientRule(from, to color.Color, width int) string {
	if width <= 0 {
		return ""
	}
	shades := lipgloss.Blend1D(max(2, width), from, to)
	var b strings.Builder
	for i := 0; i < width; i++ {
		b.WriteString(lipgloss.NewStyle().Foreground(shades[i]).Render("╱"))
	}
	return b.String()
}

// dialogFrame wraps body in the rounded frame with the gradient title rule.
// width is the TOTAL width including border and padding; the body gets
// width-4 content columns.
func dialogFrame(title string, v dialogVariant, body string, width int) string {
	t := activeTheme.Tokens
	innerW := max(10, width-4)
	titleStyle := lipgloss.NewStyle().Foreground(v.borderColor()).Bold(true).Padding(0, 1)
	rendered := titleStyle.Render(title)
	ruleW := innerW - cellWidth(rendered) - 1
	titleRow := rendered + " " + gradientRule(t.Primary, t.Secondary, ruleW)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(v.borderColor()).
		Padding(0, 1).
		Width(width).
		Render(titleRow + "\n" + body)
}

// dialogButton renders one borderless button: horizontal padding 2, focused
// ink on the button fill (error fill when the action is negative), blurred
// base ink on the subtle fill. The first character is the underlined mnemonic.
func dialogButton(label string, focused, negative bool) string {
	t := activeTheme.Tokens
	bg, fg := t.ButtonSubtle, t.FgBase
	if focused {
		bg, fg = t.Button, t.OnPrimary
		if negative {
			bg = t.Error
		}
	}
	text := label
	if runes := []rune(label); len(runes) > 0 {
		text = lipgloss.NewStyle().Underline(true).Render(string(runes[0])) + string(runes[1:])
	}
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Padding(0, 2).Render(text)
}

// badge renders a mode/status badge: bold ink on a bright fill, with the ink
// chosen by the WCAG rule so user palettes stay legible.
func badge(label string, fill color.Color) string {
	t := activeTheme.Tokens
	ink := badgeInk(t.BgLessVisible, t.OnPrimary, fill)
	return lipgloss.NewStyle().Foreground(ink).Background(fill).Bold(true).Padding(0, 1).Render(label)
}

// dialogHelp joins key/description pairs with the separator colour: keys in
// fg_more_subtle, descriptions in fg_most_subtle, one line, no wrapping.
func dialogHelp(pairs ...[2]string) string {
	t := activeTheme.Tokens
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		key := lipgloss.NewStyle().Foreground(t.FgMoreSubtle).Render(p[0])
		desc := lipgloss.NewStyle().Foreground(t.FgMostSubtle).Render(p[1])
		parts = append(parts, key+" "+desc)
	}
	sep := lipgloss.NewStyle().Foreground(t.Separator).Render(" · ")
	return strings.Join(parts, sep)
}
