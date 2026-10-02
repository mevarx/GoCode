package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// renderBanner draws the startup hero: mascot, name, tagline and session chips.
//
// The mascot leads instead of a wordmark because it is what animates all
// session — the user learns to read it before needing the name.
func renderBanner(providerName, modelName, version string, termWidth int) string {
	if termWidth > 0 && termWidth < mascotHeroWidth+4 {
		return renderCompactBanner(providerName, modelName, version, termWidth)
	}

	hero := newMascot().rest().hero(time.Now(), mascotFaceStyles)

	title := bannerTitleStyle.Render("GoCode")
	tagline := bannerTaglineStyle.Render("terminal coding agent")

	// Name and tagline align with the mascot's face, not its antenna, so the
	// pair reads as one unit rather than as text floating above a creature.
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

// mascotHeroWidth is the mascot's rendered width, including its indent.
var mascotHeroWidth = lipgloss.Width(stripANSI(mascotFaceStyles.body.Render("  ╭─────╮")))

// renderSessionChips shows the active provider and model. Naming the endpoint
// here is cheaper than having the user discover the wrong one mid-task.
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

// renderBannerHints lists the first things worth knowing. Kept to one line so
// it cannot wrap into the viewport and push the input box off screen.
func renderBannerHints() string {
	return bannerHintStyle.Render("  /help commands · Ctrl+L switch model · Ctrl+C quit")
}

// renderCompactBanner is the narrow-terminal fallback: no mascot, since a
// creature squeezed into 30 columns is noise rather than character.
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
