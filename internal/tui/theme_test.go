package tui

import (
	"testing"
)

// defaultTokenTable is the fully resolved gocode-dark palette: the
// contract every renderer and every derived style is judged against. Values
// come from the token table.
var defaultTokenTable = map[string]string{
	"primary":   "#6b50ff",
	"secondary": "#ff60ff",
	"accent":    "#68ffd6",
	"keyword":   "#ff84ff",

	"fg_base":        "#ecebf0",
	"fg_subtle":      "#bfbcc8",
	"fg_more_subtle": "#858392",
	"fg_most_subtle": "#605f6b",

	"bg_base":          "#201f26",
	"bg_most_visible":  "#4d4c57",
	"bg_less_visible":  "#3a3943",
	"bg_least_visible": "#2d2c36",

	"on_primary": "#fffaf1",
	"separator":  "#3a3943",

	"destructive":         "#ff577d",
	"destructive_subtle":  "#ff7f90",
	"error":               "#eb4268",
	"warning":             "#f5ef34",
	"warning_subtle":      "#e8fe96",
	"attention":           "#ff985a",
	"busy":                "#e8ff27",
	"info":                "#00a4ff",
	"info_more_subtle":    "#4fbefe",
	"info_most_subtle":    "#007ab8",
	"success":             "#00ffb2",
	"success_more_subtle": "#68ffd6",
	"success_most_subtle": "#12c78f",

	"diff_insert_fg":        "#629657",
	"diff_insert_code_bg":   "#303a30",
	"diff_insert_gutter_bg": "#293229",
	"diff_delete_fg":        "#a45c59",
	"diff_delete_code_bg":   "#3a3030",
	"diff_delete_gutter_bg": "#332929",

	"yolo":             "#e8fe96",
	"plan":             "#6b50ff",
	"plan_more_subtle": "#8b75ff",

	"button":          "#ff60ff",
	"button_subtle":   "#3a3943",
	"button_inactive": "#4d4c57",
	"button_hovered":  "#605f6b",

	"ansi_black":          "#2d2c36",
	"ansi_red":            "#ff577d",
	"ansi_green":          "#12c78f",
	"ansi_yellow":         "#f5ef34",
	"ansi_blue":           "#6b50ff",
	"ansi_magenta":        "#ff60ff",
	"ansi_cyan":           "#00a4ff",
	"ansi_white":          "#bfbcc8",
	"ansi_bright_black":   "#4d4c57",
	"ansi_bright_red":     "#ff6daa",
	"ansi_bright_green":   "#00ffb2",
	"ansi_bright_yellow":  "#e8fe96",
	"ansi_bright_blue":    "#7272ff",
	"ansi_bright_magenta": "#ff84ff",
	"ansi_bright_cyan":    "#4fbefe",
	"ansi_bright_white":   "#f7f6fb",
}

func TestTokenVocabularyHasFortyRolesAndSixteenANSISlots(t *testing.T) {
	if len(tokenSpecs) != 56 {
		t.Fatalf("token vocabulary has %d entries, want 56 (40 roles + 16 ANSI)", len(tokenSpecs))
	}
	seen := make(map[string]bool, len(tokenSpecs))
	for _, s := range tokenSpecs {
		if seen[s.name] {
			t.Errorf("duplicate token %q", s.name)
		}
		seen[s.name] = true
	}
	for i, name := range ansiTokenNames {
		if !seen[name] {
			t.Errorf("ANSI slot %d %q missing from the vocabulary", i, name)
		}
	}
}

func TestDefaultThemeTokenTableIsHexExact(t *testing.T) {
	th, ok := builtinTheme(DefaultThemeName)
	if !ok {
		t.Fatalf("default theme %q is not registered", DefaultThemeName)
	}
	for name, want := range defaultTokenTable {
		if got := th.Tokens.Hex(name); got != want {
			t.Errorf("token %-24s = %s, want %s", name, got, want)
		}
	}
	// Every vocabulary token must be covered by the table: a missing row would
	// silently skip the exact-value check above.
	for _, s := range tokenSpecs {
		if _, ok := defaultTokenTable[s.name]; !ok {
			t.Errorf("token %q has no expected value in the default table", s.name)
		}
	}
	if missing := th.Tokens.missingTokens(); len(missing) != 0 {
		t.Errorf("default theme leaves tokens unset: %v", missing)
	}
}

func TestDefaultThemeOverridesResolvePaletteNames(t *testing.T) {
	th, _ := builtinTheme(DefaultThemeName)
	checks := map[string]string{
		"inline code ink":     hexOf(th.Overrides.InlineCodeFg),
		"inline code fill":    hexOf(th.Overrides.InlineCodeBg),
		"link":                hexOf(th.Overrides.Link),
		"image":               hexOf(th.Overrides.Image),
		"subdued icon":        hexOf(th.Overrides.SubduedIcon),
		"prompt badge ink":    hexOf(th.Overrides.PromptBadgeFg),
		"prompt badge fill":   hexOf(th.Overrides.PromptBadgeBg),
		"prompt dots":         hexOf(th.Overrides.PromptDotsFocused),
		"prompt dots blur":    hexOf(th.Overrides.PromptDotsBlurred),
		"shell border":        hexOf(th.Overrides.ShellBorderFocused),
		"shell border blur":   hexOf(th.Overrides.ShellBorderBlurred),
		"shell prompt":        hexOf(th.Overrides.ShellPrompt),
		"syntax preproc":      hexOf(th.Overrides.Syntax.CommentPreproc),
		"syntax keyword res":  hexOf(th.Overrides.Syntax.KeywordReserved),
		"syntax keyword type": hexOf(th.Overrides.Syntax.KeywordType),
		"syntax operator":     hexOf(th.Overrides.Syntax.Operator),
		"syntax name tag":     hexOf(th.Overrides.Syntax.NameTag),
		"syntax name attr":    hexOf(th.Overrides.Syntax.NameAttribute),
		"syntax name class":   hexOf(th.Overrides.Syntax.NameClass),
		"syntax string":       hexOf(th.Overrides.Syntax.LiteralString),
	}
	want := map[string]string{
		"inline code ink":     "#ff577d", // destructive
		"inline code fill":    "#3a3943", // bg_less_visible
		"link":                "#10b1ae", // Zinc
		"image":               "#ff79d0", // Cheeky
		"subdued icon":        "#c259ff", // Violet
		"prompt badge ink":    "#f7f6fb", // Salt
		"prompt badge fill":   "#8b75ff", // Hazy
		"prompt dots":         "#8b75ff", // Hazy
		"prompt dots blur":    "#7b62ff", // Larple
		"shell border":        "#6b50ff", // Charple
		"shell border blur":   "#4d4c57", // Iron
		"shell prompt":        "#8b75ff", // Hazy
		"syntax preproc":      "#ff6e63", // Bengal
		"syntax keyword res":  "#ff4fbf", // Pony
		"syntax keyword type": "#7272ff", // Guppy
		"syntax operator":     "#ff7f90", // Salmon
		"syntax name tag":     "#d46eff", // Mauve
		"syntax name attr":    "#8b75ff", // Hazy
		"syntax name class":   "#f7f6fb", // Salt
		"syntax string":       "#bf976f", // Cumin
	}
	for name, got := range checks {
		if got != want[name] {
			t.Errorf("%s = %s, want %s", name, got, want[name])
		}
	}
}

func TestBuiltinThemesLoadAndDefineEveryToken(t *testing.T) {
	want := []struct {
		name    string
		label   string
		light   bool
		primary string
		bgBase  string
	}{
		{"gocode-dark", "GoCode Dark", false, "#6b50ff", "#201f26"},
		{"gruvbox-dark", "Gruvbox Dark", false, "#fabd2f", "#282828"},
		{"keanu", "Keanu", false, "#00ff41", "#000000"},
		{"rose-pine", "Rosé Pine", false, "#c4a7e7", "#191724"},
		{"rose-pine-moon", "Rosé Pine Moon", false, "#c4a7e7", "#232136"},
		{"rose-pine-dawn", "Rosé Pine Dawn", true, "#907aa9", "#faf4ed"},
	}
	if len(builtinThemeDefs) != len(want) {
		t.Fatalf("built-in theme count = %d, want %d", len(builtinThemeDefs), len(want))
	}
	for i, w := range want {
		def := builtinThemeDefs[i]
		if def.name != w.name {
			t.Errorf("built-in %d = %q, want %q", i, def.name, w.name)
		}
		th, ok := builtinTheme(w.name)
		if !ok {
			t.Fatalf("theme %q not found", w.name)
		}
		if th.Label != w.label {
			t.Errorf("%s label = %q, want %q", w.name, th.Label, w.label)
		}
		if th.Light != w.light {
			t.Errorf("%s light = %v, want %v", w.name, th.Light, w.light)
		}
		if got := th.Tokens.Hex("primary"); got != w.primary {
			t.Errorf("%s primary = %s, want %s", w.name, got, w.primary)
		}
		if got := th.Tokens.Hex("bg_base"); got != w.bgBase {
			t.Errorf("%s bg_base = %s, want %s", w.name, got, w.bgBase)
		}
		if missing := th.Tokens.missingTokens(); len(missing) != 0 {
			t.Errorf("%s leaves tokens unset: %v", w.name, missing)
		}
		for _, token := range []string{"diff_insert_code_bg", "diff_insert_gutter_bg", "diff_delete_code_bg", "diff_delete_gutter_bg"} {
			if th.Tokens.Hex(token) == "" {
				t.Errorf("%s has no %s", w.name, token)
			}
		}
	}
}

func TestBuiltinSpotTokensMatchVisualSpec(t *testing.T) {
	cases := []struct {
		theme string
		token string
		want  string
	}{
		{"gruvbox-dark", "fg_base", "#ebdbb2"},
		{"gruvbox-dark", "button_hovered", "#928374"},
		{"gruvbox-dark", "ansi_bright_cyan", "#8ec07c"},
		{"keanu", "keyword", "#c8ff4d"},
		{"keanu", "separator", "#183c22"},
		{"keanu", "ansi_black", "#0e2415"},
		{"rose-pine", "secondary", "#ebbcba"},
		{"rose-pine", "diff_insert_fg", "#31748f"},
		{"rose-pine", "info_most_subtle", "#46596d"},
		{"rose-pine-moon", "fg_most_subtle", "#56526e"},
		{"rose-pine-moon", "button_hovered", "#e7adb1"},
		{"rose-pine-dawn", "fg_base", "#464261"},
		{"rose-pine-dawn", "plan_more_subtle", "#cecacd"},
		{"rose-pine-dawn", "ansi_bright_white", "#464261"},
	}
	for _, tc := range cases {
		th, ok := builtinTheme(tc.theme)
		if !ok {
			t.Fatalf("theme %q not found", tc.theme)
		}
		if got := th.Tokens.Hex(tc.token); got != tc.want {
			t.Errorf("%s %s = %s, want %s", tc.theme, tc.token, got, tc.want)
		}
	}
}

func TestThemeNameMatchingIsCaseInsensitiveWithAlias(t *testing.T) {
	for _, name := range []string{"gocode-dark", "GoCode-Dark", " GOCODE-DARK ", "gocode"} {
		th, err := ResolveTheme(name)
		if err != nil {
			t.Fatalf("ResolveTheme(%q): %v", name, err)
		}
		if th.Name != DefaultThemeName {
			t.Errorf("ResolveTheme(%q) = %q, want %q", name, th.Name, DefaultThemeName)
		}
	}
	if _, err := ResolveTheme("no-such-theme"); err == nil {
		t.Fatal("unknown theme resolved without an error")
	}
}

func TestThemeCatalogListsBuiltinsInOrder(t *testing.T) {
	catalog := ThemeCatalog()
	if len(catalog) < len(builtinThemeDefs) {
		t.Fatalf("catalog has %d entries, want at least %d", len(catalog), len(builtinThemeDefs))
	}
	for i, def := range builtinThemeDefs {
		if catalog[i].Name != def.name || catalog[i].Label != def.label || catalog[i].Light != def.light {
			t.Errorf("catalog[%d] = %+v, want %s/%s/light=%v", i, catalog[i], def.name, def.label, def.light)
		}
	}
}

func TestColorSpecAcceptsEveryThemeSpelling(t *testing.T) {
	cases := map[string]string{
		"#6B50FF":   "#6b50ff",
		"#abc":      "#aabbcc",
		"#ABCD":     "#aabbcc", // alpha is flattened
		"#11223344": "#112233",
		"Charple":   "#6b50ff",
		"  smoke ":  "#bfbcc8",
		"9":         "", // ANSI index: no hex form, but must parse
		"255":       "",
	}
	for spec, want := range cases {
		c, err := parseColorSpec(spec)
		if err != nil {
			t.Fatalf("parseColorSpec(%q): %v", spec, err)
		}
		if want != "" && hexOf(c) != want {
			t.Errorf("parseColorSpec(%q) = %s, want %s", spec, hexOf(c), want)
		}
	}
	for _, bad := range []string{"", "#12", "#gggggg", "#12345", "300", "-1", "NoSuchColour"} {
		if _, err := parseColorSpec(bad); err == nil {
			t.Errorf("parseColorSpec(%q) accepted a malformed colour", bad)
		}
	}
}

func TestDialogStylesFollowThemeRebuild(t *testing.T) {
	restoreTheme(t)
	if _, err := ActivateTheme(LightThemeName); err != nil {
		t.Fatalf("activate light theme: %v", err)
	}
	if got := hexOf(colorBg); got != "#faf4ed" {
		t.Fatalf("colorBg after switch = %s, want dawn bg_base", got)
	}
	if got := hexOf(statusBarStyle.GetBackground()); got != "#f2e9e1" {
		t.Errorf("status bar background = %s, want dawn bg_least_visible", got)
	}
	if got := hexOf(inputBoxStyle.GetBorderTopForeground()); got != "#907aa9" {
		t.Errorf("focused input border = %s, want dawn primary", got)
	}
	if got := hexOf(shimmerTierStyles[0].GetForeground()); got != "#9893a5" {
		t.Errorf("shimmer dim tier = %s, want dawn fg_most_subtle", got)
	}
	if got := hexOf(shimmerTierStyles[2].GetForeground()); got != "#56949f" {
		t.Errorf("shimmer bright tier = %s, want dawn accent", got)
	}
	// And back: the default must be exactly the authored default again.
	if _, err := ActivateTheme(DefaultThemeName); err != nil {
		t.Fatalf("activate default theme: %v", err)
	}
	if got := hexOf(colorBg); got != "#201f26" {
		t.Errorf("colorBg restored = %s, want default bg_base", got)
	}
}

// restoreTheme re-installs the default theme after a test that switches it.
func restoreTheme(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := ActivateTheme(DefaultThemeName); err != nil {
			t.Errorf("restore default theme: %v", err)
		}
	})
}
