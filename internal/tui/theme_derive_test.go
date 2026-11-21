package tui

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The CIELAB tint must land on the spec's recomputed values for the default
// theme's inks; these are the reference points the fractions were fitted to.
func TestDiffTintMatchesRecomputedPaletteValues(t *testing.T) {
	insertFg := lipgloss.Color("#629657")
	deleteFg := lipgloss.Color("#A45C59")
	bg := lipgloss.Color("#201F26")

	cases := []struct {
		name     string
		fg       color.Color
		fraction float64
		want     string
	}{
		{"insert code", insertFg, diffInsertCodeFraction, "#2d3530"},
		{"insert gutter", insertFg, diffInsertGutterFraction, "#292d2c"},
		{"delete code", deleteFg, diffDeleteCodeFraction, "#33282d"},
		{"delete gutter", deleteFg, diffDeleteGutterFraction, "#2a242a"},
	}
	for _, tc := range cases {
		got := hexOf(diffTint(tc.fg, bg, tc.fraction))
		if got != tc.want {
			t.Errorf("%s tint = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// The default theme pins the fitted band constants; the derivation is what
// other themes (and user palettes) use.
func TestDefaultThemePinsFittedDiffBands(t *testing.T) {
	th, _ := builtinTheme(DefaultThemeName)
	want := map[string]string{
		"diff_insert_code_bg":   "#303a30",
		"diff_insert_gutter_bg": "#293229",
		"diff_delete_code_bg":   "#3a3030",
		"diff_delete_gutter_bg": "#332929",
	}
	for token, hex := range want {
		if got := th.Tokens.Hex(token); got != hex {
			t.Errorf("%s = %s, want fitted constant %s", token, got, hex)
		}
	}
}

func TestBuiltinThemesDeriveDiffBandsWhenNotPinned(t *testing.T) {
	gruvbox, _ := builtinTheme("gruvbox-dark")
	if got, want := gruvbox.Tokens.Hex("diff_insert_fg"), gruvbox.Tokens.Hex("success"); got != want {
		t.Errorf("gruvbox insert ink = %s, want success %s", got, want)
	}
	if got, want := gruvbox.Tokens.Hex("diff_delete_fg"), gruvbox.Tokens.Hex("destructive"); got != want {
		t.Errorf("gruvbox delete ink = %s, want destructive %s", got, want)
	}
	derived := hexOf(diffTint(gruvbox.Tokens.DiffInsertFg, gruvbox.Tokens.BgBase, diffInsertCodeFraction))
	if got := gruvbox.Tokens.Hex("diff_insert_code_bg"); got != derived {
		t.Errorf("gruvbox insert band = %s, want derived %s", got, derived)
	}
	if got := gruvbox.Tokens.Hex("diff_insert_code_bg"); got == gruvbox.Tokens.Hex("bg_base") {
		t.Error("gruvbox insert band equals the app background; the tint was not applied")
	}

	// rose-pine pins both inks but not the bands, so the bands derive from its
	// own inks and background.
	rp, _ := builtinTheme("rose-pine")
	for _, tc := range []struct {
		token    string
		fg       color.Color
		fraction float64
	}{
		{"diff_insert_code_bg", rp.Tokens.DiffInsertFg, diffInsertCodeFraction},
		{"diff_insert_gutter_bg", rp.Tokens.DiffInsertFg, diffInsertGutterFraction},
		{"diff_delete_code_bg", rp.Tokens.DiffDeleteFg, diffDeleteCodeFraction},
		{"diff_delete_gutter_bg", rp.Tokens.DiffDeleteFg, diffDeleteGutterFraction},
	} {
		if got, want := rp.Tokens.Hex(tc.token), hexOf(diffTint(tc.fg, rp.Tokens.BgBase, tc.fraction)); got != want {
			t.Errorf("rose-pine %s = %s, want %s", tc.token, got, want)
		}
	}
}

func TestDeriveTokensFillsFallbacks(t *testing.T) {
	def := themeDef{
		name:  "synthetic",
		label: "Synthetic",
		palette: map[string]string{
			"success":     "#00ff00",
			"destructive": "#ff0000",
			"bg_base":     "#101010",
		},
	}
	th := buildTheme(def)

	if got := hexOf(th.Tokens.DiffInsertFg); got != "#00ff00" {
		t.Errorf("diff insert ink = %s, want success", got)
	}
	if got := hexOf(th.Tokens.DiffDeleteFg); got != "#ff0000" {
		t.Errorf("diff delete ink = %s, want destructive", got)
	}
	if got := hexOf(th.Tokens.DestructiveSubtle); got != "#ff0000" {
		t.Errorf("destructive_subtle = %s, want destructive fallback", got)
	}
	for _, token := range []string{"diff_insert_code_bg", "diff_insert_gutter_bg", "diff_delete_code_bg", "diff_delete_gutter_bg"} {
		if th.Tokens.Hex(token) == "" {
			t.Errorf("%s was not derived", token)
		}
	}
}

func TestBadgeInkContrastRule(t *testing.T) {
	fill := lipgloss.Color("#00ffb2") // bright Julep fill
	ink := lipgloss.Color("#68ffd6")  // Bok on Julep: unreadable
	alt := lipgloss.Color("#201f26")  // dark surface ink

	if contrastRatio(ink, fill) >= minBadgeContrast {
		t.Fatalf("test setup: ink/fill contrast = %.2f, expected it below %.1f", contrastRatio(ink, fill), minBadgeContrast)
	}
	if got := hexOf(badgeInk(ink, alt, fill)); got != "#201f26" {
		t.Errorf("badge ink = %s, want the alternate ink on a low-contrast fill", got)
	}

	// Readable ink is kept even when the alternate would also read.
	readable := lipgloss.Color("#201f26")
	if got := hexOf(badgeInk(readable, lipgloss.Color("#fffaf1"), fill)); got != "#201f26" {
		t.Errorf("badge ink = %s, want the authored ink when it already contrasts", got)
	}

	// Low contrast but no better alternate: keep the authored ink rather than
	// swapping to something equally unreadable.
	if got := hexOf(badgeInk(ink, ink, fill)); got != "#68ffd6" {
		t.Errorf("badge ink = %s, want the authored ink when the alternate is not better", got)
	}
}

func TestBuiltinThemesKeepAuthoredBadgeInk(t *testing.T) {
	// The rule exists for user palettes; every built-in was authored to pass.
	for _, def := range builtinThemeDefs {
		th, _ := builtinTheme(def.name)
		if th.Overrides.QuestionBadgeInk == nil || th.Overrides.PlanBadgeInk == nil || th.Overrides.YoloInk == nil {
			t.Errorf("%s has an unset badge ink", def.name)
			continue
		}
		if c := contrastRatio(th.Overrides.QuestionBadgeInk, th.Tokens.Primary); c < minBadgeContrast {
			t.Errorf("%s question badge contrast = %.2f, want >= %.1f", def.name, c, minBadgeContrast)
		}
	}
}

func TestTextLevelsMapToTheThreeRamps(t *testing.T) {
	th, _ := builtinTheme(DefaultThemeName)
	levels := th.Tokens.TextLevels()
	if hexOf(levels.Base) != th.Tokens.Hex("fg_base") {
		t.Errorf("base level = %s, want fg_base", hexOf(levels.Base))
	}
	if hexOf(levels.Muted) != th.Tokens.Hex("fg_more_subtle") {
		t.Errorf("muted level = %s, want fg_more_subtle", hexOf(levels.Muted))
	}
	if hexOf(levels.Subtle) != th.Tokens.Hex("fg_most_subtle") {
		t.Errorf("subtle level = %s, want fg_most_subtle", hexOf(levels.Subtle))
	}
}

func TestInlineCodeSentinelIsCopyDistinguishable(t *testing.T) {
	got := inlineCode("x")
	if !strings.HasPrefix(got, "\u00a0\ufe0e") || !strings.HasSuffix(got, "\u00a0\ufe0e") {
		t.Fatalf("inlineCode(%q) = %q, want NBSP+VS15 on both sides", "x", got)
	}
	if strings.ContainsRune(got, ' ') {
		t.Error("inline code padding must not use a breakable space")
	}
	if n := len([]rune(inlineCodeSentinel)); n != 2 {
		t.Errorf("sentinel = %q has %d runes, want exactly two (NBSP + U+FE0E)", inlineCodeSentinel, n)
	}
}

func TestDiffTintHandlesNilInputs(t *testing.T) {
	if got := diffTint(nil, lipgloss.Color("#000000"), 0.2); got != nil {
		t.Errorf("diffTint(nil, bg) = %v, want nil", got)
	}
	if got := diffTint(lipgloss.Color("#ffffff"), nil, 0.2); got != nil {
		t.Errorf("diffTint(fg, nil) = %v, want nil", got)
	}
}
