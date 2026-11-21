// Derivation rules: how a palette becomes a consistent theme.
//
// The rules are design constants, not preferences: three text levels, CIELAB
// diff tints over the app background, a contrast-checked badge ink, a
// destructive-subtle fallback, and the inline-code sentinel pair.
package tui

import (
	"image/color"
	"math"

	"charm.land/lipgloss/v2"
)

// Diff tint fractions: how far each band leans from its foreground ink toward
// the app background. Code bands are stronger than their line-number gutters.
const (
	diffInsertCodeFraction   = 0.20
	diffInsertGutterFraction = 0.13
	diffDeleteCodeFraction   = 0.15
	diffDeleteGutterFraction = 0.08
)

// diffTintSteps is the resolution of the CIELAB ramp: index 0 is the
// foreground ink and index 100 is the background.
const diffTintSteps = 101

// diffTint blends a diff foreground toward bg_base in CIELAB space and returns
// the step at the given fraction. Lipgloss owns the colour maths so every
// theme lands on the same ramp; a nil input has no sensible blend and yields
// nil (the caller keeps its fallback).
func diffTint(fg, bg color.Color, fraction float64) color.Color {
	if fg == nil || bg == nil {
		return nil
	}
	steps := lipgloss.Blend1D(diffTintSteps, fg, bg)
	if len(steps) == 0 {
		return nil
	}
	idx := int(math.Round((1 - fraction) * 100))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(steps) {
		idx = len(steps) - 1
	}
	return steps[idx]
}

// minBadgeContrast is the WCAG ratio below which badge ink is unreadable on
// its fill and the alternate ink is considered.
const minBadgeContrast = 3.0

// relativeLuminance is the WCAG 2.x relative luminance of a colour, computed
// from its sRGB channels.
func relativeLuminance(c color.Color) float64 {
	if c == nil {
		return 0
	}
	r, g, b, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v) / 65535
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrastRatio is the WCAG contrast ratio between two colours, in [1, 21].
func contrastRatio(a, b color.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// badgeInk keeps a badge's ink unless it is illegible on the fill and the
// alternate ink reads better. Built-in themes therefore keep their authored
// ink; the rule exists so user palettes cannot produce unreadable badges.
func badgeInk(ink, alt, fill color.Color) color.Color {
	if ink == nil {
		return alt
	}
	base := contrastRatio(ink, fill)
	if base >= minBadgeContrast {
		return ink
	}
	if alt != nil && contrastRatio(alt, fill) > base {
		return alt
	}
	return ink
}

// inlineCodeSentinel is the no-break-space + variation-selector pair that
// pads inline code. It occupies one cell, never collapses at a line break, and
// is distinguishable when copied — unlike a normal space.
const inlineCodeSentinel = "\u00a0\ufe0e"

// inlineCode wraps code text in the sentinel pair. The padding is part of the
// string so a styled span keeps its edges even when the renderer trims spaces.
func inlineCode(s string) string {
	return inlineCodeSentinel + s + inlineCodeSentinel
}

// textLevels are the three reusable text ramps every style draws from.
type textLevels struct {
	Base   color.Color // fg_base: body text
	Muted  color.Color // fg_more_subtle: secondary text, meta rows
	Subtle color.Color // fg_most_subtle: faintest text, placeholders
}

// TextLevels exposes the three text levels for style builders.
func (t Tokens) TextLevels() textLevels {
	return textLevels{Base: t.FgBase, Muted: t.FgMoreSubtle, Subtle: t.FgMostSubtle}
}
