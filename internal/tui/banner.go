package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// Mascot leads so the user learns to read it before needing the name.
func renderBanner(providerName, modelName, version string, termWidth int) string {
	if termWidth > 0 && termWidth < mascotHeroWidth+4 {
		return renderCompactBanner(providerName, modelName, version, termWidth)
	}

	hero := newMascot().rest().hero(time.Now(), mascotFaceStyles)

	title := bannerTitleStyle.Render("GoCode")
	tagline := bannerTaglineStyle.Render("terminal coding agent")

	// Align name/tagline with the face so the pair reads as one unit.
	meta := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(mascotHeroWidth+2).Render(hero),
		lipgloss.JoinVertical(lipgloss.Left, "", "", title, tagline),
	)

	block := lipgloss.JoinVertical(
		lipgloss.Left,
		"",
		meta,
		"",
		renderSessionChips(providerName, modelName, version),
		"",
		renderBannerHints(),
	)

	if termWidth > mascotHeroWidth {
		block = lipgloss.NewStyle().
			Width(termWidth).
			Align(lipgloss.Center).
			Render(block)
	}
	return block
}

var mascotHeroWidth = lipgloss.Width(stripANSI(mascotFaceStyles.body.Render("  ╭─────╮")))

// Naming the endpoint here avoids discovering the wrong one mid-task.
func renderSessionChips(providerName, modelName, version string) string {
	chip := func(style lipgloss.Style, label, value string) string {
		if value == "" {
			return ""
		}
		return style.Render(label + value)
	}

	return "  " + strings.Join([]string{
		chip(chipVersionStyle, "v", version),
		chip(chipProviderStyle, "", providerName),
		chip(chipModelStyle, "", modelName),
	}, " ")
}

// Kept to one line so it can't wrap and push the input box off screen.
func renderBannerHints() string {
	return bannerHintStyle.Render("  /help commands · Ctrl+L switch model · Ctrl+C quit")
}

// Narrow-terminal fallback: no mascot, squeezed into 30 columns it's noise.
func renderCompactBanner(providerName, modelName, version string, termWidth int) string {
	center := lipgloss.NewStyle().Align(lipgloss.Center).Width(termWidth)

	return strings.Join([]string{
		compactTitleStyle.Render(center.Render("GoCode")),
		compactMutedStyle.Render(center.Render("terminal coding agent")),
		compactFaintStyle.Render(center.Render(fmt.Sprintf("%s · %s · v%s", providerName, modelName, version))),
		"",
		compactFaintStyle.Render(center.Render("/help for commands")),
	}, "\n")
}
