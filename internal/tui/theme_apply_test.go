package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// newThemeTestModel builds a model without touching channels a test would have
// to drain.
func newThemeTestModel() Model {
	return NewModel(
		"ollama", "llama3", "0.4.0", "",
		NewApprovalBridge(),
		make(chan string, 1),
		make(chan string, 1),
		make(chan tea.Msg, 16),
		make(chan struct{}, 1),
		[]list.Item{},
		MotionSettings{Mode: MotionAuto, SmoothStreaming: true},
	)
}

func TestGOCODETHEMEForcesPolarityTheme(t *testing.T) {
	restoreTheme(t)

	t.Setenv("GOCODE_THEME", "light")
	if got := ApplyThemeSettings("gruvbox-dark"); got.Name != LightThemeName {
		t.Fatalf("GOCODE_THEME=light picked %q, want %q", got.Name, LightThemeName)
	}
	if !ActiveTheme().Light {
		t.Error("light theme is not marked light")
	}
	if got := hexOf(colorBg); got != "#faf4ed" {
		t.Errorf("styles after light switch use bg %s, want dawn", got)
	}

	t.Setenv("GOCODE_THEME", "dark")
	if got := ApplyThemeSettings(LightThemeName); got.Name != DefaultThemeName {
		t.Fatalf("GOCODE_THEME=dark picked %q, want %q", got.Name, DefaultThemeName)
	}

	// An unrecognised value preserves the old contract: only light/dark are
	// meaningful, everything else falls through to the configured theme.
	t.Setenv("GOCODE_THEME", "banana")
	if got := ApplyThemeSettings("keanu"); got.Name != "keanu" {
		t.Fatalf("unknown GOCODE_THEME blocked the configured theme: got %q", got.Name)
	}
}

func TestConfiguredThemeAppliesWhenEnvIsUnset(t *testing.T) {
	restoreTheme(t)
	t.Setenv("GOCODE_THEME", "")

	got := ApplyThemeSettings("rose-pine-moon")
	if got.Name != "rose-pine-moon" {
		t.Fatalf("active theme = %q, want rose-pine-moon", got.Name)
	}
	if g := hexOf(statusBarStyle.GetBackground()); g != "#2c2a40" {
		t.Errorf("status bar background = %s, want moon bg_least_visible", g)
	}
}

func TestUnknownThemeFallsBackToDefault(t *testing.T) {
	restoreTheme(t)
	t.Setenv("GOCODE_THEME", "")

	got := ApplyThemeSettings("does-not-exist")
	if got.Name != DefaultThemeName {
		t.Fatalf("fallback theme = %q, want %q", got.Name, DefaultThemeName)
	}
	if ActiveTheme().Name != DefaultThemeName {
		t.Errorf("active theme = %q, want the default", ActiveTheme().Name)
	}

	// The programmatic switch reports the error instead of guessing.
	if _, err := ActivateTheme("does-not-exist"); err == nil {
		t.Fatal("ActivateTheme accepted an unknown name")
	}
	if ActiveTheme().Name != DefaultThemeName {
		t.Errorf("a failed ActivateTheme changed the active theme to %q", ActiveTheme().Name)
	}
}

func TestViewPaintsAppBackgroundEveryPath(t *testing.T) {
	restoreTheme(t)
	m := newThemeTestModel()
	m.width, m.height = 100, 30

	v := m.newView("content")
	if !v.AltScreen {
		t.Error("AltScreen must be set on the normal path")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want cell motion", v.MouseMode)
	}
	if got := hexOf(v.BackgroundColor); got != "#201f26" {
		t.Errorf("view background = %s, want default bg_base", got)
	}

	m.transparent = true
	v = m.newView("content")
	if v.BackgroundColor != nil {
		t.Errorf("transparent view paints %s, want nil", hexOf(v.BackgroundColor))
	}
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Error("AltScreen/MouseMode must survive the transparent path")
	}

	// The real View must route through the same decision.
	full := newThemeTestModel()
	full.width, full.height = 100, 30
	if got := hexOf(full.View().BackgroundColor); got != "#201f26" {
		t.Errorf("View background = %s, want default bg_base", got)
	}
}

func TestBackgroundForHonoursCapabilities(t *testing.T) {
	restoreTheme(t)
	cases := []struct {
		name        string
		transparent bool
		noColor     bool
		dumb        bool
		wantNil     bool
	}{
		{"normal", false, false, false, false},
		{"transparent", true, false, false, true},
		{"no colour", false, true, false, true},
		{"dumb terminal", false, false, true, true},
	}
	for _, tc := range cases {
		got := backgroundFor(tc.transparent, tc.noColor, tc.dumb)
		if tc.wantNil && got != nil {
			t.Errorf("%s: background = %s, want nil", tc.name, hexOf(got))
		}
		if !tc.wantNil && got == nil {
			t.Errorf("%s: background is nil, want bg_base", tc.name)
		}
	}
}

func TestApplyThemeInvalidatesCachedRenders(t *testing.T) {
	restoreTheme(t)
	m := newThemeTestModel()
	m.width = 80
	m.messages = []ChatMessage{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there"},
	}

	if out := m.renderMessages(); out == "" {
		t.Fatal("renderMessages returned nothing")
	}
	before := m.renderMessages()
	if m.cache.renders != 2 {
		t.Fatalf("cache renders = %d, want 2", m.cache.renders)
	}
	if len(m.cache.blocks) != 2 {
		t.Fatalf("cached blocks = %d, want 2", len(m.cache.blocks))
	}

	if err := m.ApplyTheme(LightThemeName); err != nil {
		t.Fatalf("ApplyTheme: %v", err)
	}
	if len(m.cache.blocks) != 0 {
		t.Errorf("theme switch kept %d cached blocks", len(m.cache.blocks))
	}
	if m.cache.width != -1 {
		t.Errorf("cache width epoch = %d, want the invalidated sentinel", m.cache.width)
	}
	if m.themeName != LightThemeName {
		t.Errorf("model theme = %q, want %q", m.themeName, LightThemeName)
	}
	if m.ThemeName() != LightThemeName {
		t.Errorf("ThemeName() = %q, want %q", m.ThemeName(), LightThemeName)
	}

	// The next render repaints from the new theme at the same width.
	after := m.renderMessages()
	if m.cache.renders != 4 {
		t.Errorf("cache renders after switch = %d, want 4 (both blocks re-rendered)", m.cache.renders)
	}
	if after == before {
		t.Error("re-render after the theme switch is byte-identical; cached colours leaked through")
	}
	if m.cache.width != 80 {
		t.Errorf("cache width after re-render = %d, want 80", m.cache.width)
	}
	if len(m.cache.blocks) != 2 {
		t.Errorf("cached blocks after re-render = %d, want 2", len(m.cache.blocks))
	}
}

func TestApplyThemeRejectsUnknownAndKeepsCache(t *testing.T) {
	restoreTheme(t)
	m := newThemeTestModel()
	m.width = 80
	m.messages = []ChatMessage{{Role: RoleUser, Content: "hello"}}
	m.renderMessages()

	if err := m.ApplyTheme("nope"); err == nil {
		t.Fatal("ApplyTheme accepted an unknown theme")
	}
	if m.themeName != DefaultThemeName {
		t.Errorf("failed switch changed the model theme to %q", m.themeName)
	}
	if len(m.cache.blocks) != 1 {
		t.Errorf("failed switch dropped cached blocks: %d", len(m.cache.blocks))
	}
}

func TestThemeNamesNeverContainSpaces(t *testing.T) {
	for _, def := range builtinThemeDefs {
		if strings.ContainsAny(def.name, " \t") {
			t.Errorf("built-in theme name %q has whitespace", def.name)
		}
		if !validThemeName(def.name) {
			t.Errorf("built-in theme name %q is not a valid theme name", def.name)
		}
	}
}
