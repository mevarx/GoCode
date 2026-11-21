package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Diff rendering: unified diffs as two-band
// tinted rows — a darker one-cell sign gutter and a lighter code band, with
// the diff foreground ink on top. Meta lines stay subtle; context lines are
// plain. The band colours are the theme's derived CIELAB tints, so every
// theme gets consistent diffs without authored constants.

// looksLikeDiff reports whether a preview string is a unified diff, so the
// approval modal can route it through the banded renderer.
func looksLikeDiff(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "diff ") {
			return true
		}
	}
	return false
}

// renderDiffView renders a unified diff at the given content width.
func renderDiffView(diff string, width int) string {
	t := activeTheme.Tokens
	w := max(2, width)
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "diff "),
			strings.HasPrefix(line, "index "):
			out = append(out, lipgloss.NewStyle().Foreground(t.FgMoreSubtle).Render(truncateToWidth(line, w)))
		case strings.HasPrefix(line, "+"):
			out = append(out, diffBand(line, t.DiffInsertFg, t.DiffInsertGutterBg, t.DiffInsertCodeBg, w))
		case strings.HasPrefix(line, "-"):
			out = append(out, diffBand(line, t.DiffDeleteFg, t.DiffDeleteGutterBg, t.DiffDeleteCodeBg, w))
		default:
			out = append(out, lipgloss.NewStyle().Foreground(t.FgBase).Render(truncateToWidth(line, w)))
		}
	}
	return strings.Join(out, "\n")
}

// diffBand paints one +/- row: the sign on the darker gutter band, the code
// on the lighter band padded to the full width so the tint reads as a block.
func diffBand(line string, fg, gutterBg, codeBg color.Color, w int) string {
	sign, rest := line[:1], line[1:]
	gutter := lipgloss.NewStyle().Foreground(fg).Background(gutterBg).Render(sign)
	code := lipgloss.NewStyle().
		Foreground(fg).
		Background(codeBg).
		Width(w - 1).
		Render(truncateToWidth(rest, w-1))
	return gutter + code
}

// renderDiffSplit is the side-by-side variant: deletes
// and context in the left column, inserts and context in the right, each half
// the width. Hunk headers span the full width above their pair of columns.
func renderDiffSplit(diff string, width int) string {
	t := activeTheme.Tokens
	half := max(4, width/2)
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	var out []string
	var left, right []string
	flush := func() {
		n := max(len(left), len(right))
		for i := 0; i < n; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out = append(out, lipgloss.JoinHorizontal(lipgloss.Top,
				lipgloss.NewStyle().Width(half).Render(l), " ",
				lipgloss.NewStyle().Width(half).Render(r)))
		}
		left, right = nil, nil
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "diff "),
			strings.HasPrefix(line, "index "):
			flush()
			out = append(out, lipgloss.NewStyle().Foreground(t.FgMoreSubtle).Render(truncateToWidth(line, width)))
		case strings.HasPrefix(line, "+"):
			right = append(right, diffBand(line, t.DiffInsertFg, t.DiffInsertGutterBg, t.DiffInsertCodeBg, half))
		case strings.HasPrefix(line, "-"):
			left = append(left, diffBand(line, t.DiffDeleteFg, t.DiffDeleteGutterBg, t.DiffDeleteCodeBg, half))
		default:
			ctx := lipgloss.NewStyle().Foreground(t.FgBase).Render(truncateToWidth(line, half))
			left = append(left, ctx)
			right = append(right, ctx)
		}
	}
	flush()
	return strings.Join(out, "\n")
}
