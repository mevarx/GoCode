// Themes: one generic builder from the token palette to the style bag, plus
// the six built-ins and the active-theme registry.
//
// Token roles, built-in palettes and derivation rules are design data; the
// implementation is original GoCode code.
package tui

import (
	"fmt"
	"image/color"
	"log/slog"
	"os"
	"strings"
)

// Theme names that are part of the public contract.
const (
	// DefaultThemeName is the built-in default theme.
	DefaultThemeName = "gocode-dark"
	// LightThemeName is the built-in light theme. Light mode is a theme, not an
	// adaptive colour switch: the UI never asks the terminal for its background.
	LightThemeName = "rose-pine-dawn"
	// defaultThemeAlias is the deprecated-but-accepted short name.
	defaultThemeAlias = "gocode"
)

// Overrides are theme-only roles that live outside the 40-token palette:
// syntax and markdown pins, inline-code colours, prompt chrome and badge inks.
// They exist so a built-in can keep an authored exception without widening the
// palette every theme must define.
type Overrides struct {
	InlineCodeFg color.Color
	InlineCodeBg color.Color

	// Markdown roles.
	Link        color.Color
	Image       color.Color
	SubduedIcon color.Color

	// Prompt chrome.
	PromptBadgeFg      color.Color
	PromptBadgeBg      color.Color
	PromptDotsFocused  color.Color
	PromptDotsBlurred  color.Color
	ShellBorderFocused color.Color
	ShellBorderBlurred color.Color
	ShellPrompt        color.Color

	// Toast and banner pins.
	StatusToastBg  color.Color
	StatusToastInk color.Color
	WarnFill       color.Color
	YoloBody       color.Color
	YoloInk        color.Color
	ToolName       color.Color
	SurfaceText    color.Color

	// Badge inks. The default is derived by badgeInk; a theme may pin it.
	QuestionBadgeInk color.Color
	PlanBadgeInk     color.Color
	H1Ink            color.Color

	Syntax Syntax
}

// Syntax is the single chroma role table. Markdown fenced blocks, file views
// and diffs all read it, so highlighting can never disagree with itself.
type Syntax struct {
	Text     color.Color
	Name     color.Color
	Operator color.Color

	Comment             color.Color
	CommentPreproc      color.Color
	NameBuiltin         color.Color
	Keyword             color.Color
	KeywordReserved     color.Color
	KeywordNamespace    color.Color
	KeywordType         color.Color
	NameAttribute       color.Color
	Punctuation         color.Color
	LiteralString       color.Color
	NameTag             color.Color
	NameClass           color.Color
	NameDecorator       color.Color
	NameFunction        color.Color
	GenericInserted     color.Color
	LiteralNumber       color.Color
	LiteralStringEscape color.Color
	GenericDeleted      color.Color
	GenericSubheading   color.Color
	Error               color.Color
	ErrorBg             color.Color
	Background          color.Color
}

// Theme is a complete, resolved theme: no token is left to be derived at
// render time.
type Theme struct {
	Name      string
	Label     string
	Light     bool
	Tokens    Tokens
	Overrides Overrides

	// patch is kept so a user theme based on this theme can rebuild the
	// overrides from its own (possibly recoloured) tokens.
	patch overridePatch
}

// ThemeInfo is the listing entry the theme picker consumes.
type ThemeInfo struct {
	Name  string
	Label string
	Light bool
}

// builtinThemesByKey is the resolved registry, keyed by normalised name.
var builtinThemesByKey = func() map[string]Theme {
	m := make(map[string]Theme, len(builtinThemeDefs))
	for _, def := range builtinThemeDefs {
		th := buildTheme(def)
		m[strings.ToLower(th.Name)] = th
	}
	return m
}()

// activeTheme is the theme every style token currently derives from. The UI
// mutates it on the update loop only, so no lock is needed; tests switch and
// restore it through ActivateTheme.
var activeTheme = builtinThemesByKey[DefaultThemeName]

func init() {
	// Kept for callers that read the capability probe; the theme itself is the
	// source of truth, not a terminal query.
	capabilities.DarkBackground = !activeTheme.Light
}

// normalizeThemeName lower-cases a theme name and maps the deprecated alias.
func normalizeThemeName(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == defaultThemeAlias {
		return DefaultThemeName
	}
	return key
}

// builtinTheme looks a built-in up by name, case-insensitively.
func builtinTheme(name string) (Theme, bool) {
	th, ok := builtinThemesByKey[normalizeThemeName(name)]
	return th, ok
}

// defaultTheme is the fallback every failed lookup lands on.
func defaultTheme() Theme { return builtinThemesByKey[DefaultThemeName] }

// ActiveTheme returns the theme styles are currently built from.
func ActiveTheme() Theme { return activeTheme }

// ResolveTheme finds a built-in or user theme by name. It reports a clear
// error for an unknown name so the caller can fall back and log.
func ResolveTheme(name string) (Theme, error) {
	key := normalizeThemeName(name)
	if key == "" {
		key = DefaultThemeName
	}
	if th, ok := builtinThemesByKey[key]; ok {
		return th, nil
	}
	themes := loadUserThemeSet()
	if th, ok := themes.byName[key]; ok {
		return th, nil
	}
	// A user theme that exists but failed to load reports its real cause.
	if err, ok := themes.errByName[key]; ok {
		return Theme{}, err
	}
	return Theme{}, fmt.Errorf("unknown theme %q (built-ins: %s)", name, strings.Join(builtinThemeNames(), ", "))
}

// builtinThemeNames returns the built-in names in canonical order.
func builtinThemeNames() []string {
	names := make([]string, 0, len(builtinThemeDefs))
	for _, def := range builtinThemeDefs {
		names = append(names, def.name)
	}
	return names
}

// ThemeCatalog lists every selectable theme: built-ins in canonical order,
// then user themes sorted by name. The picker consumes this.
func ThemeCatalog() []ThemeInfo {
	out := make([]ThemeInfo, 0, len(builtinThemeDefs)+4)
	for _, def := range builtinThemeDefs {
		out = append(out, ThemeInfo{Name: def.name, Label: def.label, Light: def.light})
	}
	themes := loadUserThemeSet()
	for _, th := range themes.themes {
		out = append(out, ThemeInfo{Name: th.Name, Label: th.Label, Light: th.Light})
	}
	return out
}

// ActivateTheme resolves a theme and rebuilds every style token from it. It is
// the programmatic switch: callers handle the error, ApplyThemeSettings logs.
func ActivateTheme(name string) (Theme, error) {
	th, err := ResolveTheme(name)
	if err != nil {
		return activeTheme, err
	}
	activateTheme(th)
	return th, nil
}

// activateTheme installs a resolved theme and repaints the shared style bag.
func activateTheme(th Theme) {
	activeTheme = th
	rebuildStyles()
	capabilities.DarkBackground = !th.Light
	// Markdown caches key on the generation, so a switch invalidates every
	// cached render and renderer without walking them.
	themeGeneration++
	invalidateMarkdownCache()
}

// ApplyThemeSettings resolves the startup theme with the documented
// precedence: GOCODE_THEME (light/dark) overrides the configured name, which
// overrides the default. A broken selection never blocks startup: it logs and
// falls back to the default theme.
func ApplyThemeSettings(configured string) Theme {
	pick := configured
	if env, ok := envThemeName(); ok {
		pick = env
	}
	th, err := ResolveTheme(pick)
	if err != nil {
		slog.Warn("theme: falling back to default", "theme", pick, "error", err)
		th = defaultTheme()
	}
	activateTheme(th)
	return th
}

// envThemeName maps GOCODE_THEME=light|dark onto its theme. Any other value is
// ignored, preserving the old contract that the variable only forces a
// background polarity.
func envThemeName() (string, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOCODE_THEME"))) {
	case "light":
		return LightThemeName, true
	case "dark":
		return DefaultThemeName, true
	}
	return "", false
}

// buildTheme resolves a built-in definition into a complete theme.
func buildTheme(def themeDef) Theme {
	var t Tokens
	set := make(map[string]bool, len(def.palette))
	for name, spec := range def.palette {
		c, err := parseColorSpec(spec)
		if err != nil {
			panic(fmt.Sprintf("tui: theme %s token %s: %v", def.name, name, err))
		}
		if !t.Set(name, c) {
			panic(fmt.Sprintf("tui: theme %s: unknown token %q", def.name, name))
		}
		set[name] = true
	}
	deriveTokens(&t, set)
	return Theme{
		Name:      def.name,
		Label:     def.label,
		Light:     def.light,
		Tokens:    t,
		Overrides: buildOverrides(t, def.patch),
		patch:     def.patch,
	}
}

// deriveTokens fills the roles a theme may leave to the derivation rules.
// set records the tokens the theme authored itself, so a pinned value (the
// default theme's fitted diff bands, for example) is never overwritten.
func deriveTokens(t *Tokens, set map[string]bool) {
	if !set["diff_insert_fg"] {
		t.DiffInsertFg = t.Success
	}
	if !set["diff_delete_fg"] {
		t.DiffDeleteFg = t.Destructive
	}
	if !set["diff_insert_code_bg"] {
		t.DiffInsertCodeBg = diffTint(t.DiffInsertFg, t.BgBase, diffInsertCodeFraction)
	}
	if !set["diff_insert_gutter_bg"] {
		t.DiffInsertGutterBg = diffTint(t.DiffInsertFg, t.BgBase, diffInsertGutterFraction)
	}
	if !set["diff_delete_code_bg"] {
		t.DiffDeleteCodeBg = diffTint(t.DiffDeleteFg, t.BgBase, diffDeleteCodeFraction)
	}
	if !set["diff_delete_gutter_bg"] {
		t.DiffDeleteGutterBg = diffTint(t.DiffDeleteFg, t.BgBase, diffDeleteGutterFraction)
	}
	if !set["destructive_subtle"] {
		t.DestructiveSubtle = t.Destructive
	}
}

// buildOverrides derives the out-of-palette roles and applies a theme's pins.
func buildOverrides(t Tokens, p overridePatch) Overrides {
	o := Overrides{
		InlineCodeFg: t.Destructive,
		InlineCodeBg: t.BgLessVisible,

		Link:        t.Info,
		Image:       t.Accent,
		SubduedIcon: t.FgMostSubtle,

		PromptBadgeFg:      t.OnPrimary,
		PromptBadgeBg:      t.Primary,
		PromptDotsFocused:  t.Primary,
		PromptDotsBlurred:  t.BgMostVisible,
		ShellBorderFocused: t.Primary,
		ShellBorderBlurred: t.BgMostVisible,
		ShellPrompt:        t.Accent,

		StatusToastBg:  t.BgLessVisible,
		StatusToastInk: t.FgBase,
		WarnFill:       t.Warning,
		YoloBody:       t.Yolo,
		ToolName:       t.Info,
		SurfaceText:    t.FgMoreSubtle,

		Syntax: buildSyntax(t),
	}
	// Fill-based badges keep a readable ink; the contrast rule only fires for
	// palettes whose authored ink would vanish on the fill.
	o.YoloInk = badgeInk(t.BgLessVisible, t.OnPrimary, t.Yolo)
	o.QuestionBadgeInk = badgeInk(t.BgLessVisible, t.OnPrimary, t.Primary)
	o.PlanBadgeInk = badgeInk(t.BgLessVisible, t.OnPrimary, t.Plan)
	o.H1Ink = badgeInk(t.BgLessVisible, t.OnPrimary, t.WarningSubtle)

	applyOverridePatch(&o, p)
	return o
}

// applyOverridePatch pins the values a built-in authors explicitly.
func applyOverridePatch(o *Overrides, p overridePatch) {
	pin := func(dst *color.Color, spec string) {
		if spec == "" {
			return
		}
		c, err := parseColorSpec(spec)
		if err != nil {
			panic("tui: bad theme override: " + err.Error())
		}
		*dst = c
	}
	pin(&o.InlineCodeFg, p.inlineCodeFg)
	pin(&o.InlineCodeBg, p.inlineCodeBg)
	pin(&o.Link, p.link)
	pin(&o.Image, p.image)
	pin(&o.SubduedIcon, p.subduedIcon)

	pin(&o.PromptBadgeFg, p.promptBadgeFg)
	pin(&o.PromptBadgeBg, p.promptBadgeBg)
	pin(&o.PromptDotsFocused, p.promptDotsFocused)
	pin(&o.PromptDotsBlurred, p.promptDotsBlurred)
	pin(&o.ShellBorderFocused, p.shellBorderFocused)
	pin(&o.ShellBorderBlurred, p.shellBorderBlurred)
	pin(&o.ShellPrompt, p.shellPrompt)

	pin(&o.StatusToastBg, p.statusToastBg)
	pin(&o.StatusToastInk, p.statusToastInk)
	pin(&o.WarnFill, p.warnFill)
	pin(&o.YoloBody, p.yoloBody)
	pin(&o.YoloInk, p.yoloInk)
	pin(&o.ToolName, p.toolName)
	pin(&o.SurfaceText, p.surfaceText)

	pin(&o.QuestionBadgeInk, p.questionBadgeInk)
	pin(&o.PlanBadgeInk, p.planBadgeInk)
	pin(&o.H1Ink, p.h1Ink)

	pin(&o.Syntax.CommentPreproc, p.syntaxCommentPreproc)
	pin(&o.Syntax.KeywordReserved, p.syntaxKeywordReserved)
	pin(&o.Syntax.KeywordNamespace, p.syntaxKeywordNamespace)
	pin(&o.Syntax.KeywordType, p.syntaxKeywordType)
	pin(&o.Syntax.Operator, p.syntaxOperator)
	pin(&o.Syntax.NameTag, p.syntaxNameTag)
	pin(&o.Syntax.NameAttribute, p.syntaxNameAttribute)
	pin(&o.Syntax.NameClass, p.syntaxNameClass)
	pin(&o.Syntax.LiteralString, p.syntaxLiteralString)
	pin(&o.Syntax.Punctuation, p.syntaxPunctuation)
}

// buildSyntax is the generic chroma role table from the
func buildSyntax(t Tokens) Syntax {
	return Syntax{
		Text:     t.FgSubtle,
		Name:     t.FgSubtle,
		Operator: t.FgSubtle,

		Comment:             t.FgMostSubtle,
		CommentPreproc:      t.Accent,
		NameBuiltin:         t.Accent,
		Keyword:             t.Info,
		KeywordReserved:     t.Keyword,
		KeywordNamespace:    t.Keyword,
		KeywordType:         t.InfoMoreSubtle,
		NameAttribute:       t.InfoMoreSubtle,
		Punctuation:         t.WarningSubtle,
		LiteralString:       t.WarningSubtle,
		NameTag:             t.Secondary,
		NameClass:           t.Primary,
		NameDecorator:       t.Attention,
		NameFunction:        t.SuccessMostSubtle,
		GenericInserted:     t.SuccessMostSubtle,
		LiteralNumber:       t.Success,
		LiteralStringEscape: t.SuccessMoreSubtle,
		GenericDeleted:      t.Destructive,
		GenericSubheading:   t.FgMoreSubtle,
		Error:               t.OnPrimary,
		ErrorBg:             t.Error,
		Background:          t.BgLessVisible,
	}
}

// missingTokens lists palette roles a theme failed to define. Built-in themes
// must define all 56: a nil role renders as "no colour", which is a silent
// visual bug rather than a compile error.
func (t Tokens) missingTokens() []string {
	var missing []string
	for _, s := range tokenSpecs {
		if s.get(t) == nil {
			missing = append(missing, s.name)
		}
	}
	return missing
}
