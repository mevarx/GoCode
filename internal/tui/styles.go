// Package tui style tokens: single source of truth for visual language.
// Direction: cool chrome, warm content — colour alone distinguishes UI from output.
//
// Every value here derives from the active theme's semantic tokens; there is no
// hex in this file. rebuildStyles is the one place styles are constructed, so
// a theme switch repaints the whole bag without touching a call site.
package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

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

// palette is the presentation view of a theme: semantic tokens grouped the way
// the existing renderers consume them. It exists so the theme refactor keeps
// the v1 style API stable; new code should read activeTheme.Tokens directly.
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

// projectPalette maps the semantic token set onto the legacy presentation
// roles. Each mapping is the nearest role: user messages speak in primary,
// assistant in accent, tool names in info, surfaces in the raised backgrounds.
func projectPalette(t Tokens) palette {
	return palette{
		bg:      t.BgBase,
		surface: t.BgLeastVisible,
		border:  t.Separator,

		text:  t.FgBase,
		muted: t.FgMoreSubtle,

		accent:    t.Primary,
		highlight: t.Secondary,

		success: t.Success,
		warning: t.Warning,
		danger:  t.Destructive,

		onAccent: t.OnPrimary,

		userBg: t.BgLessVisible,
		userFg: t.Primary,
		asstFg: t.Accent,
		toolBg: t.BgLessVisible,
		toolFg: t.Info,

		mascotBody:  t.FgMoreSubtle,
		mascotEye:   t.Primary,
		mascotMouth: t.FgMostSubtle,
		mascotTip:   t.Success,

		statusBarBg: t.BgLeastVisible,

		diffAddBg: t.DiffInsertCodeBg,
		diffAddFg: t.DiffInsertFg,
		diffDelBg: t.DiffDeleteCodeBg,
		diffDelFg: t.DiffDeleteFg,
		diffMeta:  t.FgMoreSubtle,

		shimmerDim:    t.FgMostSubtle,
		shimmerBright: t.Accent,
	}
}

// col is the presentation palette, rebuilt with every theme switch. It is read
// by the tool-card renderer and the spinner in addition to this file.
var col palette

// Colour roles. Declared here, assigned by rebuildStyles so a theme switch has
// one funnel.
var (
	// colorBg must equal app background to repaint modal region.
	colorBg      color.Color
	colorSurface color.Color

	colorBorder color.Color

	// Muted stays legible but doesn't compete with body.
	colorText    color.Color
	colorMutedFg color.Color

	// Distinct hues so focused/selected never read as same.
	colorAccent    color.Color
	colorHighlight color.Color

	// Separate hues from speech roles: green here means fine, there means assistant.
	colorSuccess color.Color
	colorWarning color.Color
	colorDanger  color.Color

	// Stays light in both themes to keep contrast on saturated fills.
	colorOnAccent color.Color

	// Each speaker owns a hue; fills only for quoted user/tool text.
	colorUserBg color.Color
	colorUserFg color.Color
	colorAsstFg color.Color
	colorToolBg color.Color
	colorToolFg color.Color
)

// Style bag. Same names as v1 so renderers and tests keep compiling.
var (
	statusBarStyle       lipgloss.Style
	statusProviderStyle  lipgloss.Style
	statusModelStyle     lipgloss.Style
	statusPathStyle      lipgloss.Style
	statusStreamingStyle lipgloss.Style
	statusMetricsStyle   lipgloss.Style
	statusSeparator      string

	userLabelStyle     lipgloss.Style
	userBubbleStyle    lipgloss.Style
	asstLabelStyle     lipgloss.Style
	asstContentStyle   lipgloss.Style
	toolLabelStyle     lipgloss.Style
	toolDoneLabelStyle lipgloss.Style
	toolBubbleStyle    lipgloss.Style
	toolBodyStyle      lipgloss.Style
	toolFaintStyle     lipgloss.Style
	spinnerSuffixStyle lipgloss.Style
	errorStyle         lipgloss.Style
	systemStyle        lipgloss.Style

	inputBoxStyle     lipgloss.Style
	inputBoxBlurStyle lipgloss.Style
	inputHintStyle    lipgloss.Style
	steerChipStyle    lipgloss.Style

	modalOverlayStyle  lipgloss.Style
	modalTitleStyle    lipgloss.Style
	modalToolNameStyle lipgloss.Style
	modalArgKeyStyle   lipgloss.Style
	modalArgValStyle   lipgloss.Style
	modalButtonApprove lipgloss.Style
	modalButtonDeny    lipgloss.Style
	modalButtonFocused lipgloss.Style

	colorMascotBody  color.Color
	colorMascotEye   color.Color
	colorMascotMouth color.Color
	colorMascotTip   color.Color

	// Struct so one sprite renders in multiple palettes (tests use unstyled).
	mascotFaceStyles mascotStyles

	bannerTitleStyle   lipgloss.Style
	bannerTaglineStyle lipgloss.Style
	bannerHintStyle    lipgloss.Style
	chipTextStyle      lipgloss.Style
	chipVersionStyle   lipgloss.Style
	chipProviderStyle  lipgloss.Style
	chipModelStyle     lipgloss.Style
	compactTitleStyle  lipgloss.Style
	compactMutedStyle  lipgloss.Style
	compactFaintStyle  lipgloss.Style
)

func init() {
	rebuildStyles()
}

// mascotStyles carries the four mascot roles as independent styles, so one
// sprite can render in any palette.
type mascotStyles struct {
	body  lipgloss.Style
	eye   lipgloss.Style
	mouth lipgloss.Style
	tip   lipgloss.Style
}

// rebuildStyles is the theme → style derivation function: one pass, every
// token. It runs at package init and on each ActivateTheme.
func rebuildStyles() {
	col = projectPalette(activeTheme.Tokens)

	// Colour roles.
	colorBg = col.bg
	colorSurface = col.surface
	colorBorder = col.border
	colorText = col.text
	colorMutedFg = col.muted
	colorAccent = col.accent
	colorHighlight = col.highlight
	colorSuccess = col.success
	colorWarning = col.warning
	colorDanger = col.danger
	colorOnAccent = col.onAccent
	colorUserBg = col.userBg
	colorUserFg = col.userFg
	colorAsstFg = col.asstFg
	colorToolBg = col.toolBg
	colorToolFg = col.toolFg

	// Status bar. Side padding is load-bearing for renderStatusBar width math.
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

	// Turn metrics are secondary information: muted like the workspace path.
	statusMetricsStyle = statusBarStyle.
		Foreground(colorMutedFg)

	// Shimmer band: same fill and weight as the bar, three brightness tiers.
	shimmerTierStyles = [3]lipgloss.Style{
		lipgloss.NewStyle().Background(col.statusBarBg).Bold(true).Foreground(col.shimmerDim),
		lipgloss.NewStyle().Background(col.statusBarBg).Bold(true).Foreground(col.accent),
		lipgloss.NewStyle().Background(col.statusBarBg).Bold(true).Foreground(col.shimmerBright),
	}

	// Labels margin-free; bodies share one gutter for a single aligned column.
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

	// A finished tool card leads with its ✓, so the label carries the success
	// role instead of the tool hue; the body stays in the tool bubble.
	toolDoneLabelStyle = lipgloss.NewStyle().
		Foreground(colorSuccess).
		Bold(true)

	toolBubbleStyle = lipgloss.NewStyle().
		Foreground(colorToolFg).
		Background(colorToolBg).
		Padding(0, rowPadCompact).
		MarginLeft(indentGutter)

	// The tool-card body sits indented on the less-visible
	// surface; params render faint inside it.
	toolBodyStyle = lipgloss.NewStyle().
		Background(activeTheme.Tokens.BgLessVisible).
		Padding(0, rowPadCompact).
		MarginLeft(indentGutter)

	toolFaintStyle = lipgloss.NewStyle().
		Foreground(activeTheme.Tokens.FgMoreSubtle)

	// The spinner's elapsed suffix is data, not decoration: faint like the
	// params so it never competes with the cycling glyphs.
	spinnerSuffixStyle = lipgloss.NewStyle().
		Foreground(activeTheme.Tokens.FgMoreSubtle)

	// Gutter already in text prefix; margin would indent twice.
	errorStyle = lipgloss.NewStyle().
		Foreground(colorDanger).
		Bold(true)

	systemStyle = lipgloss.NewStyle().
		Foreground(colorMutedFg).
		Italic(true)

	// Focus is hue plus fill so it survives flattened colour; blurred keeps same geometry.
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

	// Queued-steering indicator: visible but quieter than the input border.
	steerChipStyle = lipgloss.NewStyle().
		Foreground(colorAccent).
		PaddingLeft(rowPadCompact)

	// Neutral panel; danger signal lives in title/deny button only.
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

	// Same chrome as UI; only eye/tip saturated.
	colorMascotBody = col.mascotBody
	colorMascotEye = col.mascotEye
	colorMascotMouth = col.mascotMouth
	colorMascotTip = col.mascotTip

	// Struct so one sprite renders in multiple palettes (tests use unstyled).
	mascotFaceStyles = mascotStyles{
		body:  lipgloss.NewStyle().Foreground(colorMascotBody),
		eye:   lipgloss.NewStyle().Foreground(colorMascotEye).Bold(true),
		mouth: lipgloss.NewStyle().Foreground(colorMascotMouth),
		tip:   lipgloss.NewStyle().Foreground(colorMascotTip).Bold(true),
	}

	// Startup banner tokens.
	bannerTitleStyle = lipgloss.NewStyle().Foreground(colorText).Bold(true)

	bannerTaglineStyle = lipgloss.NewStyle().Foreground(colorMutedFg).Italic(true)

	bannerHintStyle = lipgloss.NewStyle().Foreground(colorMutedFg).Italic(true)

	// Pill text contrasts with own background, not app background.
	chipTextStyle = lipgloss.NewStyle().Foreground(colorBg).Bold(true).Padding(0, rowPadCompact)

	chipVersionStyle = chipTextStyle.Background(colorAccent)
	chipProviderStyle = chipTextStyle.Background(colorSuccess)
	chipModelStyle = chipTextStyle.Background(colorWarning)

	compactTitleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	compactMutedStyle = lipgloss.NewStyle().Foreground(colorMutedFg)
	compactFaintStyle = lipgloss.NewStyle().Foreground(colorMutedFg)
}
