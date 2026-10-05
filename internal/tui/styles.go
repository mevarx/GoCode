// Package tui style tokens: single source of truth for visual language.
// Direction: cool chrome, warm content — colour alone distinguishes UI from output.
package tui

import "charm.land/lipgloss/v2"

// Gutter is 2 cells to match marker glyphs; content indents, labels must not.
const (
	indentGutter = 2
	// One space reads as padding, two as a panel.
	rowPadCompact = 1
	// Extra space makes buttons look pressable.
	rowPadAction = 2
	// Matching weights so rules/dividers read as one UI language.
	separatorGlyph = "─"
	statusSepGlyph = "│"
)

// v1 role names kept; values come from theme.go palette. No hex here.
var (
	// colorBg must equal app background to repaint modal region.
	colorBg      = col.bg
	colorSurface = col.surface

	colorBorder = col.border

	// Muted stays legible but doesn't compete with body.
	colorText    = col.text
	colorMutedFg = col.muted

	// Distinct hues so focused/selected never read as same.
	colorAccent    = col.accent
	colorHighlight = col.highlight

	// Separate hues from speech roles: green here means fine, there means assistant.
	colorSuccess = col.success
	colorWarning = col.warning
	colorDanger  = col.danger

	// Stays light in both themes to keep contrast on saturated fills.
	colorOnAccent = col.onAccent

	// Each speaker owns a hue; fills only for quoted user/tool text.
	colorUserBg = col.userBg
	colorUserFg = col.userFg
	colorAsstFg = col.asstFg
	colorToolBg = col.toolBg
	colorToolFg = col.toolFg
)

// Side padding is load-bearing for renderStatusBar width math.
var (
	statusBarStyle = lipgloss.NewStyle().
			Background(col.statusBarBg).
			Foreground(colorMutedFg).
			Padding(0, rowPadCompact)

	statusProviderStyle = statusBarStyle.
				Foreground(colorAccent).
				Bold(true)

	statusModelStyle = statusBarStyle.
				Foreground(colorHighlight)

	statusPathStyle = statusBarStyle.
			Foreground(colorMutedFg)

	// Warning, not accent, so running turn differs from provider name.
	statusStreamingStyle = statusBarStyle.
				Foreground(colorWarning).
				Bold(true)

	// Padding already spaces glyph; no extra spaces or gap triples.
	statusSeparator = statusBarStyle.
			Foreground(colorBorder).
			Render(statusSepGlyph)
)

// Labels margin-free; bodies share one gutter for a single aligned column.
var (
	userLabelStyle = lipgloss.NewStyle().
			Foreground(colorUserFg).
			Bold(true)

	userBubbleStyle = lipgloss.NewStyle().
			Foreground(colorUserFg).
			Background(colorUserBg).
			Padding(0, rowPadCompact).
			MarginLeft(indentGutter)

	asstLabelStyle = lipgloss.NewStyle().
			Foreground(colorAsstFg).
			Bold(true)

	// No padding or wrap width shrinks and it misaligns with quoted bubbles.
	asstContentStyle = lipgloss.NewStyle().
				Foreground(colorText).
				MarginLeft(indentGutter)

	toolLabelStyle = lipgloss.NewStyle().
			Foreground(colorToolFg).
			Bold(true)

	toolBubbleStyle = lipgloss.NewStyle().
			Foreground(colorToolFg).
			Background(colorToolBg).
			Padding(0, rowPadCompact).
			MarginLeft(indentGutter)

	// Gutter already in text prefix; margin would indent twice.
	errorStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true)

	systemStyle = lipgloss.NewStyle().
			Foreground(colorMutedFg).
			Italic(true)
)

// Focus is hue plus fill so it survives flattened colour; blurred keeps same geometry.
var (
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Background(colorSurface).
			Padding(0, rowPadCompact)

	inputBoxBlurStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(0, rowPadCompact)

	inputHintStyle = lipgloss.NewStyle().
			Foreground(colorMutedFg).
			Italic(true)
)

// Neutral panel; danger signal lives in title/deny button only.
var (
	modalOverlayStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(1, rowPadAction).
				Background(colorBg)

	// No margin; body already gaps after title.
	modalTitleStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true)

	modalToolNameStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	// Muted keys, body values so args read as pairs.
	modalArgKeyStyle = lipgloss.NewStyle().
				Foreground(colorMutedFg)

	modalArgValStyle = lipgloss.NewStyle().
				Foreground(colorText)

	modalButtonApprove = lipgloss.NewStyle().
				Background(colorSuccess).
				Foreground(colorOnAccent).
				Bold(true).
				Padding(0, rowPadAction)

	modalButtonDeny = lipgloss.NewStyle().
			Background(colorDanger).
			Foreground(colorOnAccent).
			Bold(true).
			Padding(0, rowPadAction)

	// Underline, not border: border misaligns; underline survives flattened colour.
	modalButtonFocused = lipgloss.NewStyle().
				Background(colorAccent).
				Foreground(colorOnAccent).
				Bold(true).
				Padding(0, rowPadAction).
				Underline(true)
)

// Same chrome as UI; only eye/tip saturated.
var (
	colorMascotBody  = col.mascotBody
	colorMascotEye   = col.mascotEye
	colorMascotMouth = col.mascotMouth
	colorMascotTip   = col.mascotTip
)

// Struct so one sprite renders in multiple palettes (tests use unstyled).
type mascotStyles struct {
	body  lipgloss.Style
	eye   lipgloss.Style
	mouth lipgloss.Style
	tip   lipgloss.Style
}

var mascotFaceStyles = mascotStyles{
	body:  lipgloss.NewStyle().Foreground(colorMascotBody),
	eye:   lipgloss.NewStyle().Foreground(colorMascotEye).Bold(true),
	mouth: lipgloss.NewStyle().Foreground(colorMascotMouth),
	tip:   lipgloss.NewStyle().Foreground(colorMascotTip).Bold(true),
}

// Startup banner tokens.
var (
	bannerTitleStyle = lipgloss.NewStyle().Foreground(colorText).Bold(true)

	bannerTaglineStyle = lipgloss.NewStyle().Foreground(colorMutedFg).Italic(true)

	bannerHintStyle = lipgloss.NewStyle().Foreground(colorMutedFg).Italic(true)

	// Pill text contrasts with own background, not app background.
	chipTextStyle = lipgloss.NewStyle().Foreground(colorBg).Bold(true).Padding(0, rowPadCompact)

	chipVersionStyle  = chipTextStyle.Background(colorAccent)
	chipProviderStyle = chipTextStyle.Background(colorSuccess)
	chipModelStyle    = chipTextStyle.Background(colorWarning)

	compactTitleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	compactMutedStyle = lipgloss.NewStyle().Foreground(colorMutedFg)
	compactFaintStyle = lipgloss.NewStyle().Foreground(colorMutedFg)
)
