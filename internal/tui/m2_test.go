package tui

import (
	"fmt"
	"strings"
	"testing"
)

// contracts: theme-driven markdown with stable-prefix caching, banded
// diffs, and the tool-card anatomy (plain head, tinted body, faint
// params). All colour assertions derive from the active theme tokens.

func sgrFg(c interface{ RGBA() (r, g, b, a uint32) }) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func sgrBg(c interface{ RGBA() (r, g, b, a uint32) }) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func m2Model(t *testing.T) Model {
	t.Helper()
	m := newTestModel(nil)
	m.width, m.height, m.ready = 140, 40, true
	return m
}

func TestMarkdownRendersWithThemeColors(t *testing.T) {
	m := m2Model(t)
	out := m.renderMarkdown("# Title\n\nsome `code` here")
	if !strings.Contains(out, sgrFg(activeTheme.Tokens.Primary)) {
		t.Errorf("heading does not use the primary token: %q", out)
	}
	if !strings.Contains(out, "\u00a0\ufe0e") {
		t.Errorf("inline code missing the NBSP+FE0E sentinel: %q", out)
	}
	inlineFg := activeTheme.Tokens.Keyword
	if activeTheme.Overrides.InlineCodeFg != nil {
		inlineFg = activeTheme.Overrides.InlineCodeFg
	}
	if !strings.Contains(out, sgrFg(inlineFg)) {
		t.Errorf("inline code does not use the resolved inline fg: %q", out)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "code") {
		t.Errorf("content lost in render: %q", out)
	}
}

func TestMarkdownStablePrefixCache(t *testing.T) {
	m := m2Model(t)
	invalidateMarkdownCache()
	content := "a stable paragraph of text"
	first := m.renderMarkdown(content)
	second := m.renderMarkdown(content)
	if first != second {
		t.Fatal("identical content rendered differently")
	}
	mdState.Lock()
	renders, renderers := len(mdState.renders), len(mdState.renderers)
	mdState.Unlock()
	if renders != 1 || renderers != 1 {
		t.Errorf("cache not reused: %d renders, %d renderers, want 1/1", renders, renderers)
	}
	// Growing the prefix is a new key; the old one stays cached.
	m.renderMarkdown(content + " more")
	mdState.Lock()
	renders = len(mdState.renders)
	mdState.Unlock()
	if renders != 2 {
		t.Errorf("grown prefix should add one cache entry, got %d", renders)
	}
}

func TestMarkdownThemeInvalidation(t *testing.T) {
	m := m2Model(t)
	before := themeGeneration
	m.renderMarkdown("# cached")
	if _, err := ActivateTheme("gruvbox-dark"); err != nil {
		t.Fatalf("activate gruvbox: %v", err)
	}
	defer ActivateTheme("gocode-dark")
	if themeGeneration <= before {
		t.Fatal("theme activation did not bump the generation")
	}
	mdState.Lock()
	n := len(mdState.renders)
	mdState.Unlock()
	if n != 0 {
		t.Errorf("theme switch left %d cached renders", n)
	}
	out := m.renderMarkdown("# cached")
	if !strings.Contains(out, sgrFg(activeTheme.Tokens.Primary)) {
		t.Errorf("re-render does not use the new theme primary: %q", out)
	}
}

func TestMarkdownSanitizesUntrustedInput(t *testing.T) {
	m := m2Model(t)
	out := m.renderMarkdown("hello \x1b[31mINJECTED\x1b[0m world")
	if strings.Contains(out, "\x1b[31mINJECTED") {
		t.Errorf("injected SGR survived sanitization: %q", out)
	}
	if !strings.Contains(out, "INJECTED") {
		t.Errorf("visible text lost: %q", out)
	}
}

func TestDiffViewBands(t *testing.T) {
	tok := activeTheme.Tokens
	out := renderDiffView("@@ -1,2 +1,2 @@\n-old line\n+new line\n context", 40)
	if !strings.Contains(out, sgrBg(tok.DiffInsertGutterBg)) || !strings.Contains(out, sgrBg(tok.DiffInsertCodeBg)) {
		t.Errorf("insert row missing the two-band tints: %q", out)
	}
	if !strings.Contains(out, sgrBg(tok.DiffDeleteGutterBg)) || !strings.Contains(out, sgrBg(tok.DiffDeleteCodeBg)) {
		t.Errorf("delete row missing the two-band tints: %q", out)
	}
	if !strings.Contains(out, sgrFg(tok.FgMoreSubtle)) {
		t.Errorf("hunk header not subtle: %q", out)
	}
	if !strings.Contains(out, "new line") || !strings.Contains(out, "old line") {
		t.Errorf("diff content lost: %q", out)
	}
}

func TestLooksLikeDiff(t *testing.T) {
	for _, s := range []string{"@@ -1 +1 @@", "--- a/f\n+++ b/f", "diff --git a/f b/f"} {
		if !looksLikeDiff(s) {
			t.Errorf("looksLikeDiff(%q) = false", s)
		}
	}
	if looksLikeDiff("plain preview text") {
		t.Error("plain text classified as diff")
	}
}

func TestToolCardBodyTintedHeadPlain(t *testing.T) {
	out := toolCardLines("⠋ running…\n{\"path\":\"a\"}")
	head, body, ok := strings.Cut(out, "\n")
	if !ok {
		t.Fatalf("no body line: %q", out)
	}
	if strings.Contains(head, sgrBg(activeTheme.Tokens.BgLessVisible)) {
		t.Errorf("head should stay on the base surface: %q", head)
	}
	if !strings.Contains(body, sgrBg(activeTheme.Tokens.BgLessVisible)) {
		t.Errorf("body missing the less-visible tint: %q", body)
	}
	if toolCardLines("head only") != "head only" {
		t.Error("head-only card was altered")
	}
}

func TestToolCardParamsFaint(t *testing.T) {
	out := toolCardContent("⠋ running…", `{"a":1}`)
	if !strings.Contains(out, sgrFg(activeTheme.Tokens.FgMoreSubtle)) {
		t.Errorf("params not faint: %q", out)
	}
}
