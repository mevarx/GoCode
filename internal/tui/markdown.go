package tui

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"github.com/mevarx/GoCode/internal/safetext"
)

// Markdown rendering for assistant messages: one glamour renderer per (width, theme generation), one shared chroma
// role table (the theme's Syntax overrides) so fenced blocks, file views and
// diffs can never disagree, and a stable-prefix content cache so settled
// messages render once and only the live tail re-renders as it grows.

// themeGeneration increments on every theme activation; it is part of every
// markdown cache key, so a switch invalidates without walking the caches.
var themeGeneration uint64

type mdKey struct {
	content string
	width   int
	gen     uint64
}

type rendererKey struct {
	width int
	gen   uint64
}

var mdState = struct {
	sync.Mutex
	renderers map[rendererKey]*glamour.TermRenderer
	renders   map[mdKey]string
}{
	renderers: make(map[rendererKey]*glamour.TermRenderer),
	renders:   make(map[mdKey]string),
}

// mdCacheLimit bounds the content cache; a bulk drop is cheap because the
// block cache above it already prevents settled messages from re-calling.
const mdCacheLimit = 256

// invalidateMarkdownCache drops every cached render and renderer. Called on
// theme activation alongside the generation bump.
func invalidateMarkdownCache() {
	mdState.Lock()
	defer mdState.Unlock()
	mdState.renderers = make(map[rendererKey]*glamour.TermRenderer)
	mdState.renders = make(map[mdKey]string)
}

// markdownWidth resolves the wrap width for assistant content: the chat
// content cap (<=120) minus the gutter, with a sane floor before the first
// resize event.
func (m *Model) markdownWidth() int {
	w := m.chatContentWidth() - 2
	if w <= 0 {
		w = 80
	}
	return max(20, min(120, w))
}

// renderMarkdown sanitizes untrusted model text, then renders it through the
// theme-configured glamour renderer. Identical (content, width, generation)
// triples reuse the cached render — the stable-prefix contract.
func (m *Model) renderMarkdown(content string) string {
	content = safetext.Sanitize(content)
	if strings.TrimSpace(content) == "" {
		return ""
	}
	width := m.markdownWidth()
	key := mdKey{content: content, width: width, gen: themeGeneration}

	mdState.Lock()
	if out, ok := mdState.renders[key]; ok {
		mdState.Unlock()
		return out
	}
	rkey := rendererKey{width: width, gen: themeGeneration}
	r, ok := mdState.renderers[rkey]
	if !ok {
		var err error
		r, err = glamour.NewTermRenderer(
			glamour.WithStyles(themeMarkdownStyles()),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			mdState.Unlock()
			return content
		}
		mdState.renderers[rkey] = r
	}
	mdState.Unlock()

	out, err := r.Render(content)
	if err != nil {
		return content
	}
	out = strings.TrimRight(out, "\n")

	mdState.Lock()
	if len(mdState.renders) >= mdCacheLimit {
		mdState.renders = make(map[mdKey]string)
	}
	mdState.renders[key] = out
	mdState.Unlock()
	return out
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func prim(c interface{ RGBA() (r, g, b, a uint32) }) ansi.StylePrimitive {
	return ansi.StylePrimitive{Color: strPtr(hexOf(c))}
}

// themeMarkdownStyles builds the glamour style config from the active theme.
// Every colour comes from the resolved tokens/overrides — no hardcoded hex.
func themeMarkdownStyles() ansi.StyleConfig {
	t := activeTheme.Tokens
	o := activeTheme.Overrides

	inlineFg, inlineBg := t.Keyword, t.BgLeastVisible
	if o.InlineCodeFg != nil {
		inlineFg = o.InlineCodeFg
	}
	if o.InlineCodeBg != nil {
		inlineBg = o.InlineCodeBg
	}
	link := t.Info
	if o.Link != nil {
		link = o.Link
	}
	image := t.Secondary
	if o.Image != nil {
		image = o.Image
	}

	cfg := ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: prim(t.FgBase),
			Margin:         uintPtr(0),
		},
		// Text stays unset so it inherits the enclosing block's colour —
		// setting it here would flatten headings and quotes to the base ink.
		Heading: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(hexOf(t.Primary)), Bold: boolPtr(true)}},
		H1:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(hexOf(t.Primary)), Bold: boolPtr(true)}},
		H2:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(hexOf(t.Secondary)), Bold: boolPtr(true)}},
		H3:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(hexOf(t.Secondary)), Bold: boolPtr(true)}},
		Emph:    ansi.StylePrimitive{Color: strPtr(hexOf(t.FgSubtle)), Italic: boolPtr(true)},
		Strong:  ansi.StylePrimitive{Color: strPtr(hexOf(t.FgBase)), Bold: boolPtr(true)},
		Link:    ansi.StylePrimitive{Color: strPtr(hexOf(link)), Underline: boolPtr(true)},
		LinkText: ansi.StylePrimitive{
			Color: strPtr(hexOf(link)),
		},
		Image:     prim(image),
		ImageText: prim(image),
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: strPtr(hexOf(t.FgMoreSubtle)), BlockPrefix: "│ "},
		},
		List:        ansi.StyleList{StyleBlock: ansi.StyleBlock{StylePrimitive: prim(t.FgSubtle)}},
		Item:        prim(t.FgSubtle),
		Enumeration: prim(t.FgSubtle),
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:           strPtr(hexOf(inlineFg)),
				BackgroundColor: strPtr(hexOf(inlineBg)),
				// The inline-code sentinel: a no-break space + U+FE0E
				// on each side — one cell, non-breaking, copy-distinguishable.
				Prefix: "\u00a0\ufe0e",
				Suffix: "\u00a0\ufe0e",
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: prim(t.FgBase),
				Margin:         uintPtr(0),
			},
			Chroma: themeChroma(),
		},
		HorizontalRule: ansi.StylePrimitive{Color: strPtr(hexOf(t.Separator)), Format: "─"},
		Strikethrough:  ansi.StylePrimitive{CrossedOut: boolPtr(true)},
	}
	return cfg
}

func uintPtr(u uint) *uint { return &u }

// themeChroma projects the theme's single Syntax role table onto glamour's
// chroma config. Nil roles stay unset so glamour's defaults apply.
func themeChroma() *ansi.Chroma {
	s := activeTheme.Overrides.Syntax
	c := &ansi.Chroma{}
	set := func(dst *ansi.StylePrimitive, col interface{ RGBA() (r, g, b, a uint32) }) {
		if col != nil {
			dst.Color = strPtr(hexOf(col))
		}
	}
	set(&c.Text, s.Text)
	set(&c.Error, s.Error)
	set(&c.Comment, s.Comment)
	set(&c.CommentPreproc, s.CommentPreproc)
	set(&c.Keyword, s.Keyword)
	set(&c.KeywordReserved, s.KeywordReserved)
	set(&c.KeywordNamespace, s.KeywordNamespace)
	set(&c.KeywordType, s.KeywordType)
	set(&c.Operator, s.Operator)
	set(&c.Punctuation, s.Punctuation)
	set(&c.Name, s.Name)
	set(&c.NameBuiltin, s.NameBuiltin)
	set(&c.NameTag, s.NameTag)
	set(&c.NameAttribute, s.NameAttribute)
	set(&c.NameClass, s.NameClass)
	set(&c.NameDecorator, s.NameDecorator)
	set(&c.NameFunction, s.NameFunction)
	set(&c.LiteralNumber, s.LiteralNumber)
	set(&c.LiteralString, s.LiteralString)
	set(&c.LiteralStringEscape, s.LiteralStringEscape)
	set(&c.GenericInserted, s.GenericInserted)
	set(&c.GenericDeleted, s.GenericDeleted)
	set(&c.GenericSubheading, s.GenericSubheading)
	if s.Background != nil {
		c.Background.BackgroundColor = strPtr(hexOf(s.Background))
	}
	return c
}
