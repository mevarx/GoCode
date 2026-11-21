package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

// setUserThemeDir points ConfigDir (and therefore the themes directory) at a
// throwaway root. Both platform env vars are set so the test is identical on
// Windows and POSIX.
func setUserThemeDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := themesDir()
	if dir != filepath.Join(config.ConfigDir(), "themes") {
		t.Fatalf("themesDir = %q, want under %q", dir, config.ConfigDir())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create themes dir: %v", err)
	}
	return dir
}

// writeUserTheme writes one theme document by name.
func writeUserTheme(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write theme %s: %v", name, err)
	}
}

func TestUserThemeOverridesBaseTokens(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "mytheme", `{"base":"gruvbox-dark","primary":"#123456","bg_base":"#010203"}`)

	th, err := ResolveTheme("mytheme")
	if err != nil {
		t.Fatalf("resolve user theme: %v", err)
	}
	if th.Name != "mytheme" || th.Label != "mytheme" {
		t.Errorf("theme identity = %q/%q, want mytheme", th.Name, th.Label)
	}
	if got := th.Tokens.Hex("primary"); got != "#123456" {
		t.Errorf("primary = %s, want override #123456", got)
	}
	if got := th.Tokens.Hex("bg_base"); got != "#010203" {
		t.Errorf("bg_base = %s, want override #010203", got)
	}
	// Untouched tokens keep the base theme's values.
	if got := th.Tokens.Hex("fg_base"); got != "#ebdbb2" {
		t.Errorf("fg_base = %s, want gruvbox #ebdbb2", got)
	}
	if th.Light {
		t.Error("a theme based on gruvbox-dark must not be marked light")
	}

	found := false
	for _, info := range ThemeCatalog() {
		if info.Name == "mytheme" {
			found = true
		}
	}
	if !found {
		t.Error("user theme missing from the catalog")
	}
}

func TestUserThemeDefaultsToDefaultBase(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "loose", `{"fg_base":"#abcdef"}`)

	th, err := ResolveTheme("loose")
	if err != nil {
		t.Fatalf("resolve user theme: %v", err)
	}
	if got := th.Tokens.Hex("bg_base"); got != "#201f26" {
		t.Errorf("bg_base = %s, want default theme background", got)
	}
	if got := th.Tokens.Hex("fg_base"); got != "#abcdef" {
		t.Errorf("fg_base = %s, want override", got)
	}
}

func TestUserThemeUnknownBaseIsAClearError(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "broken", `{"base":"does-not-exist","primary":"#ffffff"}`)

	themes, errs := loadUserThemes()
	if len(themes) != 0 {
		t.Fatalf("broken theme was loaded: %+v", themes)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `unknown base theme "does-not-exist"`) {
		t.Fatalf("errors = %v, want a clear unknown-base error", errs)
	}
	if _, err := ResolveTheme("broken"); err == nil {
		t.Error("a broken user theme resolved without an error")
	}
}

func TestUserThemeMalformedHexIsAClearError(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "badhex", `{"base":"rose-pine","primary":"#12345"}`)

	themes, errs := loadUserThemes()
	if len(themes) != 0 {
		t.Fatalf("malformed theme was loaded: %+v", themes)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "malformed hex colour") {
		t.Fatalf("errors = %v, want a malformed hex error", errs)
	}
	// Selecting the broken theme must surface the real reason, not "unknown".
	if _, err := ResolveTheme("badhex"); err == nil || !strings.Contains(err.Error(), "malformed hex colour") {
		t.Fatalf("ResolveTheme(badhex) error = %v, want the parse failure", err)
	}
}

func TestUserThemeUnknownTokenIsRejected(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "typo", `{"base":"rose-pine","primari":"#ffffff"}`)

	_, errs := loadUserThemes()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `unknown token "primari"`) {
		t.Fatalf("errors = %v, want an unknown-token error", errs)
	}
}

func TestUserThemeNonStringValueIsRejected(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "numeric", `{"primary":123}`)

	_, errs := loadUserThemes()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "must be a colour string") {
		t.Fatalf("errors = %v, want a string-value error", errs)
	}
}

func TestUserThemeBaseCycleIsRejected(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "alpha", `{"base":"beta"}`)
	writeUserTheme(t, dir, "beta", `{"base":"alpha"}`)

	themes, errs := loadUserThemes()
	if len(themes) != 0 {
		t.Fatalf("cyclic themes were loaded: %+v", themes)
	}
	if len(errs) == 0 {
		t.Fatal("cycle was not reported")
	}
	joined := ""
	for _, err := range errs {
		joined += err.Error() + "\n"
	}
	if !strings.Contains(joined, "base cycle") {
		t.Fatalf("errors = %v, want a base-cycle error", errs)
	}
}

func TestUserThemeCanExtendAnotherUserTheme(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "child", `{"base":"parent","bg_base":"#222222"}`)
	writeUserTheme(t, dir, "parent", `{"base":"rose-pine","primary":"#ff00ff"}`)

	child, err := ResolveTheme("child")
	if err != nil {
		t.Fatalf("resolve child: %v", err)
	}
	if got := child.Tokens.Hex("primary"); got != "#ff00ff" {
		t.Errorf("child primary = %s, want inherited #ff00ff", got)
	}
	if got := child.Tokens.Hex("bg_base"); got != "#222222" {
		t.Errorf("child bg_base = %s, want own override", got)
	}
	if got := child.Tokens.Hex("fg_base"); got != "#e0def4" {
		t.Errorf("child fg_base = %s, want rose-pine base", got)
	}
}

func TestUserThemeDiffBandsFollowRecoloredInk(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "reddiff", `{"base":"gocode-dark","diff_insert_fg":"#ff0000"}`)

	th, err := ResolveTheme("reddiff")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := hexOf(diffTint(th.Tokens.DiffInsertFg, th.Tokens.BgBase, diffInsertCodeFraction))
	if got := th.Tokens.Hex("diff_insert_code_bg"); got != want {
		t.Errorf("insert band = %s, want derived %s", got, want)
	}
	if got := th.Tokens.Hex("diff_insert_code_bg"); got == "#303a30" {
		t.Error("band kept the base theme's fitted constant after the ink was recoloured")
	}
	// The delete side was untouched, so its fitted bands survive.
	if got := th.Tokens.Hex("diff_delete_code_bg"); got != "#3a3030" {
		t.Errorf("delete band = %s, want the base theme's fitted value", got)
	}
}

func TestUserThemeUnrelatedOverrideKeepsFittedBands(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "recolor", `{"base":"gocode-dark","primary":"#00ff00"}`)

	th, err := ResolveTheme("recolor")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := th.Tokens.Hex("diff_insert_code_bg"); got != "#303a30" {
		t.Errorf("insert band = %s, want the fitted constant", got)
	}
}

func TestUserThemeInvalidFileNameIsSkipped(t *testing.T) {
	dir := setUserThemeDir(t)
	writeUserTheme(t, dir, "bad name", `{"base":"rose-pine"}`)

	themes, errs := loadUserThemes()
	if len(themes) != 0 {
		t.Fatalf("invalid name was loaded: %+v", themes)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "invalid theme name") {
		t.Fatalf("errors = %v, want an invalid-name error", errs)
	}
}

func TestMissingThemeDirIsNotAnError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	// Deliberately do not create the directory.
	themes, errs := loadUserThemes()
	if len(themes) != 0 || len(errs) != 0 {
		t.Fatalf("missing theme dir produced themes=%v errs=%v, want neither", themes, errs)
	}
	if len(ThemeCatalog()) != len(builtinThemeDefs) {
		t.Errorf("catalog = %d entries, want only the built-ins", len(ThemeCatalog()))
	}
}
