// Package tui style tokens: the single source of truth for the TUI's visual
// language. Colour lives here and nowhere else, so call sites never write hex,
// every colour is a role rather than a pigment, and all of it is AdaptiveColor
// because a TUI is not assumed to run on a dark terminal.
//
// Direction: cool chrome, warm content — anything the agent did is amber or red,
// so colour alone tells you whether you are looking at UI or at output.
package tui

import "github.com/charmbracelet/lipgloss"

// Layout constants.
//
// The gutter is fixed at 2 cells to match the marker glyphs the renderer writes
// into its own label strings. Styles drawing message *content* indent by
// indentGutter; styles drawing labels must not, or they indent twice.
const (
	// indentGutter is the left gutter for message content.
	indentGutter = 2
	// rowPadCompact is horizontal padding for single-line rows (bubbles,
	// status bar cells): one space reads as padding, two reads as a panel.
	rowPadCompact = 1
	// rowPadAction is horizontal padding for things the user clicks or picks
	// (modal buttons): the extra space makes them look pressable.
	rowPadAction = 2
	// separatorGlyph is the horizontal rule; statusSepGlyph is the inline
	// divider. Matching weights keep rules and dividers from looking like two
	// different UI languages.
	separatorGlyph = "─"
	statusSepGlyph = "│"
)

// Semantic colour ramp.
var (
	// Surfaces. colorBg repaints the region a modal occupies, so it must equal
	// the app background; colorSurface lifts a focused control off it.
	colorBg      = lipgloss.AdaptiveColor{Light: "#f6f8fa", Dark: "#0d1117"}
	colorSurface = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#161b22"}

	colorBorder = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#30363d"}

	// Text ramp. colorText is body copy; colorMutedFg is metadata that must stay
	// legible but must not compete with it.
	colorText    = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e6edf3"}
	colorMutedFg = lipgloss.AdaptiveColor{Light: "#59636e", Dark: "#8b949e"}

	// Interactive accent (chrome) and selection highlight. Distinct hues so
	// "focused" and "selected" never read as the same state.
	colorAccent    = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"}
	colorHighlight = lipgloss.AdaptiveColor{Light: "#8250df", Dark: "#bc8cff"}

	// Status semantics. Deliberately separate hues from the speech-role colours
	// below: green here means "fine", green there means "the assistant spoke".
	colorSuccess = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
	colorWarning = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#e3b341"}
	colorDanger  = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#ff7b72"}

	// colorOnAccent is text sitting on a saturated fill, so it stays light in
	// both themes rather than flipping to a theme-matched value that would lose
	// contrast against the fill itself.
	colorOnAccent = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#ffffff"}

	// Speech roles. Each speaker owns a hue so a transcript can be scanned by
	// colour alone, with a tinted fill only for the two sides that are quoted
	// text (user input and tool output) rather than narration.
	colorUserBg = lipgloss.AdaptiveColor{Light: "#d0f2f7", Dark: "#0b2b30"}
	colorUserFg = lipgloss.AdaptiveColor{Light: "#0b7285", Dark: "#66d9e8"}
	colorAsstFg = lipgloss.AdaptiveColor{Light: "#2b8a3e", Dark: "#8ce99a"}
	colorToolBg = lipgloss.AdaptiveColor{Light: "#fff4d6", Dark: "#33280a"}
	colorToolFg = lipgloss.AdaptiveColor{Light: "#b06000", Dark: "#ffc078"}
)

// Status bar.
//
// The one cell of padding on each side is load-bearing: renderStatusBar pads its
// right segment to contentWidth and relies on it to land at the terminal width.
var (
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#eaeef2", Dark: "#161b22"}).
			Foreground(colorMutedFg).
			Padding(0, rowPadCompact)

	statusProviderStyle = statusBarStyle.
				Foreground(colorAccent).
				Bold(true)

	statusModelStyle = statusBarStyle.
				Foreground(colorHighlight)

	statusPathStyle = statusBarStyle.
			Foreground(colorMutedFg)

	// Streaming gets the warning hue, not the accent, so a running turn is
	// distinguishable from the provider name it sits next to.
	statusStreamingStyle = statusBarStyle.
				Foreground(colorWarning).
				Bold(true)

	// The bar's own padding spaces the glyph, so the literal is the divider
	// alone; adding spaces here would triple the gap between segments.
	statusSeparator = statusBarStyle.
			Foreground(colorBorder).
			Render(statusSepGlyph)
)

// Transcript.
//
// Labels are margin-free by design (see indentGutter). Bodies all share one
// gutter so a transcript reads as a single aligned column.
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

	// The assistant body is full-bleed prose, so it takes no horizontal
	// padding: padding it would also shrink its wrap width and push it out of
	// alignment with the quoted bubbles, which are inset to read as quotes.
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

	// Single-line roles carry their gutter in the rendered text itself (the
	// renderer writes a two-space prefix), so these must stay margin-free or
	// they indent twice and sit a column right of every multi-line message.
	errorStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true)

	systemStyle = lipgloss.NewStyle().
			Foreground(colorMutedFg).
			Italic(true)
)

// Input.
//
// Focus is signalled by border hue *and* a raised fill, not hue alone: on
// terminals that flatten colour to plain text, or for readers who cannot
// separate the accent from the border, the fill change still shows focus. The
// blurred box keeps the same geometry so typing never reflows the view.
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

// Approval modal.
//
// The panel itself is neutral rather than red: the same overlay frames the
// model picker, and a danger-washed surface would make a routine choice look
// like an emergency. The danger signal is carried by the title and the deny
// button, which only ever appear on the approval path.
var (
	modalOverlayStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(1, rowPadAction).
				Background(colorBg)

	// No bottom margin here: the modal body already inserts a blank line after
	// the title. Two owners for one gap is how a dialog ends up with a hole in
	// it.
	modalTitleStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true)

	modalToolNameStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	// Keys are muted and values take body colour, so the argument table reads
	// as label/value pairs rather than a wall of equally loud text.
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

	// Focus is an underline rather than a border: a border adds a row and would
	// knock this button out of alignment with its unbordered neighbours, and the
	// underline survives terminals that flatten colour to plain text.
	modalButtonFocused = lipgloss.NewStyle().
				Background(colorAccent).
				Foreground(colorOnAccent).
				Bold(true).
				Padding(0, rowPadAction).
				Underline(true)
)

// separator renders a full-width horizontal rule. Narrow terminals still get a
// single cell rather than an empty string, so callers can rely on the rule
// occupying a line.

// Mascot palette.
//
// The mascot uses the same chrome colours as the rest of the UI so it reads as
// part of the application. Only the eye and antenna tip are saturated.
var (
	colorMascotBody  = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#7d8590"}
	colorMascotEye   = lipgloss.AdaptiveColor{Light: "#005f87", Dark: "#58a6ff"}
	colorMascotMouth = lipgloss.AdaptiveColor{Light: "#57606a", Dark: "#8b949e"}
	colorMascotTip   = lipgloss.AdaptiveColor{Light: "#0f7b3f", Dark: "#3fb950"}
)

// mascotStyles is the mascot's colour scheme, held as a struct so one sprite
// can be rendered in more than one palette (the tests render it unstyled).
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

	// Session chips are filled pills, so their text must contrast with their
	// own background rather than with the app background.
	chipTextStyle = lipgloss.NewStyle().Foreground(colorBg).Bold(true).Padding(0, rowPadCompact)

	chipVersionStyle  = chipTextStyle.Background(colorAccent)
	chipProviderStyle = chipTextStyle.Background(colorSuccess)
	chipModelStyle    = chipTextStyle.Background(colorWarning)

	compactTitleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	compactMutedStyle = lipgloss.NewStyle().Foreground(colorMutedFg)
	compactFaintStyle = lipgloss.NewStyle().Foreground(colorMutedFg)
)
