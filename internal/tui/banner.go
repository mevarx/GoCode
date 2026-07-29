package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var asciiLines = []string{
	` ██████╗  ██████╗  ██████╗ ██████╗ ██████╗ ███████╗`,
	`██╔════╝ ██╔═══██╗██╔════╝██╔═══██╗██╔══██╗██╔════╝`,
	`██║  ███╗██║   ██║██║     ██║   ██║██║  ██║█████╗  `,
	`██║   ██║██║   ██║██║     ██║   ██║██║  ██║██╔══╝  `,
	`╚██████╔╝╚██████╔╝╚██████╗╚██████╔╝██████╔╝███████╗`,
	` ╚═════╝  ╚═════╝  ╚═════╝ ╚═════╝ ╚═════╝ ╚══════╝`,
}

var gradientColors = []string{
	"#00d4ff",
	"#00b4ff",
	"#0090ff",
	"#5f7fff",
	"#8f5fff",
	"#bf4fff",
}

func renderBanner(providerName, modelName, version string, termWidth int) string {
	logoWidth := lipgloss.Width(asciiLines[0])

	var renderedLines []string
	for _, line := range asciiLines {
		renderedLines = append(renderedLines, colorizeASCIILine(line, gradientColors))
	}
	logo := strings.Join(renderedLines, "\n")

	tagline := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6e7681")).
		Italic(true).
		Render("  Terminal coding agent — local-first, provider-agnostic")

	versionChip := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#0d1117")).
		Background(lipgloss.Color("#58a6ff")).
		Bold(true).
		Padding(0, 1).
		Render("v" + version)

	providerPill := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#0d1117")).
		Background(lipgloss.Color("#3fb950")).
		Bold(true).
		Padding(0, 1).
		Render(providerName)

	modelPill := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#0d1117")).
		Background(lipgloss.Color("#e3b341")).
		Bold(true).
		Padding(0, 1).
		Render(modelName)

	infoLine := fmt.Sprintf("  %s  %s  %s", versionChip, providerPill, modelPill)

	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#484f58")).
		Italic(true)
	hints := hintStyle.Render("  /clear · /provider · /model · exit")

	divW := logoWidth
	if termWidth > 0 && termWidth < divW {
		divW = termWidth
	}
	divider := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#21262d")).
		Render(strings.Repeat("─", divW))

	block := lipgloss.JoinVertical(lipgloss.Left,
		"",
		logo,
		tagline,
		"",
		infoLine,
		"",
		hints,
		divider,
		"",
	)

	if termWidth > logoWidth {
		block = lipgloss.NewStyle().
			Width(termWidth).
			Align(lipgloss.Center).
			Render(block)
	}

	return block
}

func colorizeASCIILine(line string, colors []string) string {
	runes := []rune(line)
	total := len(runes)
	if total == 0 {
		return ""
	}
	n := len(colors)
	segSize := total / n
	if segSize < 1 {
		segSize = 1
	}

	var sb strings.Builder
	for i, c := range colors {
		start := i * segSize
		end := start + segSize
		if i == n-1 {
			end = total
		}
		if start >= total {
			break
		}
		seg := string(runes[start:end])
		styled := lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(seg)
		sb.WriteString(styled)
	}
	return sb.String()
}
