// Package tui theme: single source of truth for colour.
// v2 has no AdaptiveColor; palette is resolved once from background.
package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Every colour role for one background; no hex outside this file.
type palette struct {
	// Surfaces.
	bg      color.Color
	surface color.Color
	border  color.Color

	// Text ramp.
	text  color.Color
	muted color.Color

	// Interactive accent (chrome) and selection highlight.
	accent    color.Color
	highlight color.Color

	// Status semantics.
	success color.Color
	warning color.Color
	danger  color.Color

	onAccent color.Color

	// Speech roles.
	userBg color.Color
	userFg color.Color
	asstFg color.Color
	toolBg color.Color
	toolFg color.Color

	// Mascot.
	mascotBody  color.Color
	mascotEye   color.Color
	mascotMouth color.Color
	mascotTip   color.Color

	// Distinct from app background so bar reads as chrome.
	statusBarBg color.Color

	// Low-saturation fills so layered syntax stays legible.
	diffAddBg color.Color
	diffAddFg color.Color
	diffDelBg color.Color
	diffDelFg color.Color
	diffMeta  color.Color

	// Ramp so animated text reads as moving light.
	shimmerDim    color.Color
	shimmerBright color.Color
}

// Keeps previous AdaptiveColor ramps, so migration has no visual drift.
func newPalette(isDark bool) palette {
	ld := lipgloss.LightDark(isDark)
	c := func(light, dark string) color.Color {
		return ld(lipgloss.Color(light), lipgloss.Color(dark))
	}

	return palette{
		bg:      c("#f6f8fa", "#0d1117"),
		surface: c("#ffffff", "#161b22"),
		border:  c("#d0d7de", "#30363d"),

		text:  c("#1f2328", "#e6edf3"),
		muted: c("#59636e", "#8b949e"),

		accent:    c("#0969da", "#58a6ff"),
		highlight: c("#8250df", "#bc8cff"),

		success: c("#1a7f37", "#3fb950"),
		warning: c("#9a6700", "#e3b341"),
		danger:  c("#cf222e", "#ff7b72"),

		onAccent: c("#ffffff", "#ffffff"),

		userBg: c("#d0f2f7", "#0b2b30"),
		userFg: c("#0b7285", "#66d9e8"),
		asstFg: c("#2b8a3e", "#8ce99a"),
		toolBg: c("#fff4d6", "#33280a"),
		toolFg: c("#b06000", "#ffc078"),

		mascotBody:  c("#6e7781", "#7d8590"),
		mascotEye:   c("#005f87", "#58a6ff"),
		mascotMouth: c("#57606a", "#8b949e"),
		mascotTip:   c("#0f7b3f", "#3fb950"),

		statusBarBg: c("#eaeef2", "#161b22"),

		diffAddBg: c("#e6ffec", "#12261e"),
		diffAddFg: c("#1a7f37", "#7ee787"),
		diffDelBg: c("#ffebe9", "#2d1418"),
		diffDelFg: c("#cf222e", "#ffa198"),
		diffMeta:  c("#59636e", "#8b949e"),

		shimmerDim:    c("#8c959f", "#6e7681"),
		shimmerBright: c("#0969da", "#79c0ff"),
	}
}

// GOCODE_THEME=light|dark overrides detection for tests and misreported terminals.
// Otherwise single query only on TTY; pipes default to dark.
var darkBackground = resolveDarkBackground()

// col is the resolved palette, read by styles.go and the tool-card renderer.
var col = newPalette(darkBackground)

func init() {
	// Publishes DarkBackground back to capability.go.
	capabilities.DarkBackground = darkBackground
}

func resolveDarkBackground() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOCODE_THEME"))) {
	case "light":
		return false
	case "dark":
		return true
	}
	if !capabilities.IsTTY {
		return true
	}
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}

// v2 replacement for AdaptiveColor: picks via resolved background.
func adaptive(light, dark string) color.Color {
	return lipgloss.LightDark(darkBackground)(lipgloss.Color(light), lipgloss.Color(dark))
}
