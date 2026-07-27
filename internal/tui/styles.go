package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorBg        = lipgloss.AdaptiveColor{Light: "#f5f5f5", Dark: "#0d1117"}
	colorBorder    = lipgloss.AdaptiveColor{Light: "#d0d0d0", Dark: "#30363d"}
	colorUserBg    = lipgloss.AdaptiveColor{Light: "#dff0ff", Dark: "#0d2137"}
	colorUserFg    = lipgloss.AdaptiveColor{Light: "#005f87", Dark: "#5fd7ff"}
	colorAsstFg    = lipgloss.AdaptiveColor{Light: "#005f00", Dark: "#a8e6a8"}
	colorToolFg    = lipgloss.AdaptiveColor{Light: "#875f00", Dark: "#ffcc66"}
	colorToolBg    = lipgloss.AdaptiveColor{Light: "#fff8e1", Dark: "#1a1400"}
	colorMutedFg   = lipgloss.AdaptiveColor{Light: "#707070", Dark: "#6e7681"}
	colorAccent    = lipgloss.AdaptiveColor{Light: "#005f87", Dark: "#58a6ff"}
	colorDanger    = lipgloss.AdaptiveColor{Light: "#cc0000", Dark: "#f85149"}
	colorSuccess   = lipgloss.AdaptiveColor{Light: "#005f00", Dark: "#3fb950"}
	colorWarning   = lipgloss.AdaptiveColor{Light: "#875f00", Dark: "#e3b341"}
	colorHighlight = lipgloss.AdaptiveColor{Light: "#005f87", Dark: "#79c0ff"}
)

var (
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#e0e0e0", Dark: "#161b22"}).
			Foreground(colorMutedFg).
			Padding(0, 1)

	statusProviderStyle = statusBarStyle.
				Foreground(colorAccent).
				Bold(true)

	statusModelStyle = statusBarStyle.
				Foreground(colorHighlight)

	statusStreamingStyle = statusBarStyle.
				Foreground(colorWarning).
				Bold(true)

	statusSeparator = statusBarStyle.
			Foreground(colorBorder).
			Render(" │ ")
)

var (
	userLabelStyle = lipgloss.NewStyle().
			Foreground(colorUserFg).
			Bold(true)

	userBubbleStyle = lipgloss.NewStyle().
			Foreground(colorUserFg).
			Background(colorUserBg).
			Padding(0, 1).
			MarginLeft(2)

	asstLabelStyle = lipgloss.NewStyle().
			Foreground(colorAsstFg).
			Bold(true)

	asstContentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#e6edf3"}).
				MarginLeft(2)

	toolLabelStyle = lipgloss.NewStyle().
			Foreground(colorToolFg).
			Bold(true)

	toolBubbleStyle = lipgloss.NewStyle().
			Foreground(colorToolFg).
			Background(colorToolBg).
			Padding(0, 1).
			MarginLeft(2)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true).
			MarginLeft(2)

	systemStyle = lipgloss.NewStyle().
			Foreground(colorMutedFg).
			Italic(true).
			MarginLeft(2)

	separatorStyle = lipgloss.NewStyle().
			Foreground(colorBorder)
)

var (
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)

	inputBoxBlurStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

	inputHintStyle = lipgloss.NewStyle().
			Foreground(colorMutedFg).
			Italic(true)
)

var (
	modalOverlayStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(colorDanger).
				Padding(1, 2).
				Background(lipgloss.AdaptiveColor{Light: "#fff5f5", Dark: "#1a0000"})

	modalTitleStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true).
			MarginBottom(1)

	modalToolNameStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	modalArgKeyStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg)

	modalArgValStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#e6edf3"})

	modalButtonApprove = lipgloss.NewStyle().
				Background(colorSuccess).
				Foreground(lipgloss.Color("#ffffff")).
				Bold(true).
				Padding(0, 2)

	modalButtonDeny = lipgloss.NewStyle().
			Background(colorDanger).
			Foreground(lipgloss.Color("#ffffff")).
			Bold(true).
			Padding(0, 2)

	modalButtonFocused = lipgloss.NewStyle().
				Background(colorAccent).
				Foreground(lipgloss.Color("#ffffff")).
				Bold(true).
				Padding(0, 2).
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("#ffffff"))
)

func separator(width int) string {
	if width < 1 {
		width = 1
	}
	line := ""
	for i := 0; i < width; i++ {
		line += "─"
	}
	return separatorStyle.Render(line)
}
