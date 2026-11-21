// Package tui colour vocabulary.
//
// Tokens are the semantic roles every theme is expressed in; the named palette
// below is plain colour data (names to hex). The token list and derivation rules
// are design data; the implementation is original GoCode code.
package tui

import (
	"errors"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// Tokens is one fully resolved palette: every semantic role the UI paints
// with, plus the 16-slot ANSI remap for raw terminal output.
//
// A theme never stores partial colours: missing roles are filled by the
// derivation rules in derive.go before a Theme reaches the renderer.
type Tokens struct {
	// Brand.
	Primary   color.Color
	Secondary color.Color
	Accent    color.Color
	Keyword   color.Color

	// Text ramp.
	FgBase       color.Color
	FgSubtle     color.Color
	FgMoreSubtle color.Color
	FgMostSubtle color.Color

	// Surfaces, strongest last.
	BgBase         color.Color
	BgMostVisible  color.Color
	BgLessVisible  color.Color
	BgLeastVisible color.Color

	OnPrimary color.Color
	Separator color.Color

	// Status semantics.
	Destructive       color.Color
	DestructiveSubtle color.Color
	Error             color.Color
	Warning           color.Color
	WarningSubtle     color.Color
	Attention         color.Color
	Busy              color.Color
	Info              color.Color
	InfoMoreSubtle    color.Color
	InfoMostSubtle    color.Color
	Success           color.Color
	SuccessMoreSubtle color.Color
	SuccessMostSubtle color.Color

	// Diffs: foreground ink and the two tinted bands per side.
	DiffInsertFg       color.Color
	DiffInsertCodeBg   color.Color
	DiffInsertGutterBg color.Color
	DiffDeleteFg       color.Color
	DiffDeleteCodeBg   color.Color
	DiffDeleteGutterBg color.Color

	// Mode chrome.
	Yolo           color.Color
	Plan           color.Color
	PlanMoreSubtle color.Color

	// Buttons.
	Button         color.Color
	ButtonSubtle   color.Color
	ButtonInactive color.Color
	ButtonHovered  color.Color

	// ANSI is the 0-15 remap table for raw terminal output.
	ANSI [16]color.Color
}

// ANSI slot names in index order.
var ansiTokenNames = [16]string{
	"ansi_black",
	"ansi_red",
	"ansi_green",
	"ansi_yellow",
	"ansi_blue",
	"ansi_magenta",
	"ansi_cyan",
	"ansi_white",
	"ansi_bright_black",
	"ansi_bright_red",
	"ansi_bright_green",
	"ansi_bright_yellow",
	"ansi_bright_blue",
	"ansi_bright_magenta",
	"ansi_bright_cyan",
	"ansi_bright_white",
}

// tokenSpec binds one JSON/token name to its field, keeping the override
// loader, the user-theme writer and the tests on one vocabulary.
type tokenSpec struct {
	name string
	get  func(Tokens) color.Color
	set  func(*Tokens, color.Color)
}

func spec(name string, get func(Tokens) color.Color, set func(*Tokens, color.Color)) tokenSpec {
	return tokenSpec{name: name, get: get, set: set}
}

func ansiSpec(i int) tokenSpec {
	return spec(ansiTokenNames[i],
		func(t Tokens) color.Color { return t.ANSI[i] },
		func(t *Tokens, c color.Color) { t.ANSI[i] = c })
}

// tokenSpecs is the canonical order from the: 40 semantic
// roles followed by the 16 ANSI slots (56 keys total).
var tokenSpecs = func() []tokenSpec {
	specs := []tokenSpec{
		spec("primary", func(t Tokens) color.Color { return t.Primary }, func(t *Tokens, c color.Color) { t.Primary = c }),
		spec("secondary", func(t Tokens) color.Color { return t.Secondary }, func(t *Tokens, c color.Color) { t.Secondary = c }),
		spec("accent", func(t Tokens) color.Color { return t.Accent }, func(t *Tokens, c color.Color) { t.Accent = c }),
		spec("keyword", func(t Tokens) color.Color { return t.Keyword }, func(t *Tokens, c color.Color) { t.Keyword = c }),
		spec("fg_base", func(t Tokens) color.Color { return t.FgBase }, func(t *Tokens, c color.Color) { t.FgBase = c }),
		spec("fg_subtle", func(t Tokens) color.Color { return t.FgSubtle }, func(t *Tokens, c color.Color) { t.FgSubtle = c }),
		spec("fg_more_subtle", func(t Tokens) color.Color { return t.FgMoreSubtle }, func(t *Tokens, c color.Color) { t.FgMoreSubtle = c }),
		spec("fg_most_subtle", func(t Tokens) color.Color { return t.FgMostSubtle }, func(t *Tokens, c color.Color) { t.FgMostSubtle = c }),
		spec("bg_base", func(t Tokens) color.Color { return t.BgBase }, func(t *Tokens, c color.Color) { t.BgBase = c }),
		spec("bg_most_visible", func(t Tokens) color.Color { return t.BgMostVisible }, func(t *Tokens, c color.Color) { t.BgMostVisible = c }),
		spec("bg_less_visible", func(t Tokens) color.Color { return t.BgLessVisible }, func(t *Tokens, c color.Color) { t.BgLessVisible = c }),
		spec("bg_least_visible", func(t Tokens) color.Color { return t.BgLeastVisible }, func(t *Tokens, c color.Color) { t.BgLeastVisible = c }),
		spec("on_primary", func(t Tokens) color.Color { return t.OnPrimary }, func(t *Tokens, c color.Color) { t.OnPrimary = c }),
		spec("separator", func(t Tokens) color.Color { return t.Separator }, func(t *Tokens, c color.Color) { t.Separator = c }),
		spec("destructive", func(t Tokens) color.Color { return t.Destructive }, func(t *Tokens, c color.Color) { t.Destructive = c }),
		spec("destructive_subtle", func(t Tokens) color.Color { return t.DestructiveSubtle }, func(t *Tokens, c color.Color) { t.DestructiveSubtle = c }),
		spec("error", func(t Tokens) color.Color { return t.Error }, func(t *Tokens, c color.Color) { t.Error = c }),
		spec("warning", func(t Tokens) color.Color { return t.Warning }, func(t *Tokens, c color.Color) { t.Warning = c }),
		spec("warning_subtle", func(t Tokens) color.Color { return t.WarningSubtle }, func(t *Tokens, c color.Color) { t.WarningSubtle = c }),
		spec("attention", func(t Tokens) color.Color { return t.Attention }, func(t *Tokens, c color.Color) { t.Attention = c }),
		spec("busy", func(t Tokens) color.Color { return t.Busy }, func(t *Tokens, c color.Color) { t.Busy = c }),
		spec("info", func(t Tokens) color.Color { return t.Info }, func(t *Tokens, c color.Color) { t.Info = c }),
		spec("info_more_subtle", func(t Tokens) color.Color { return t.InfoMoreSubtle }, func(t *Tokens, c color.Color) { t.InfoMoreSubtle = c }),
		spec("info_most_subtle", func(t Tokens) color.Color { return t.InfoMostSubtle }, func(t *Tokens, c color.Color) { t.InfoMostSubtle = c }),
		spec("success", func(t Tokens) color.Color { return t.Success }, func(t *Tokens, c color.Color) { t.Success = c }),
		spec("success_more_subtle", func(t Tokens) color.Color { return t.SuccessMoreSubtle }, func(t *Tokens, c color.Color) { t.SuccessMoreSubtle = c }),
		spec("success_most_subtle", func(t Tokens) color.Color { return t.SuccessMostSubtle }, func(t *Tokens, c color.Color) { t.SuccessMostSubtle = c }),
		spec("diff_insert_fg", func(t Tokens) color.Color { return t.DiffInsertFg }, func(t *Tokens, c color.Color) { t.DiffInsertFg = c }),
		spec("diff_insert_code_bg", func(t Tokens) color.Color { return t.DiffInsertCodeBg }, func(t *Tokens, c color.Color) { t.DiffInsertCodeBg = c }),
		spec("diff_insert_gutter_bg", func(t Tokens) color.Color { return t.DiffInsertGutterBg }, func(t *Tokens, c color.Color) { t.DiffInsertGutterBg = c }),
		spec("diff_delete_fg", func(t Tokens) color.Color { return t.DiffDeleteFg }, func(t *Tokens, c color.Color) { t.DiffDeleteFg = c }),
		spec("diff_delete_code_bg", func(t Tokens) color.Color { return t.DiffDeleteCodeBg }, func(t *Tokens, c color.Color) { t.DiffDeleteCodeBg = c }),
		spec("diff_delete_gutter_bg", func(t Tokens) color.Color { return t.DiffDeleteGutterBg }, func(t *Tokens, c color.Color) { t.DiffDeleteGutterBg = c }),
		spec("yolo", func(t Tokens) color.Color { return t.Yolo }, func(t *Tokens, c color.Color) { t.Yolo = c }),
		spec("plan", func(t Tokens) color.Color { return t.Plan }, func(t *Tokens, c color.Color) { t.Plan = c }),
		spec("plan_more_subtle", func(t Tokens) color.Color { return t.PlanMoreSubtle }, func(t *Tokens, c color.Color) { t.PlanMoreSubtle = c }),
		spec("button", func(t Tokens) color.Color { return t.Button }, func(t *Tokens, c color.Color) { t.Button = c }),
		spec("button_subtle", func(t Tokens) color.Color { return t.ButtonSubtle }, func(t *Tokens, c color.Color) { t.ButtonSubtle = c }),
		spec("button_inactive", func(t Tokens) color.Color { return t.ButtonInactive }, func(t *Tokens, c color.Color) { t.ButtonInactive = c }),
		spec("button_hovered", func(t Tokens) color.Color { return t.ButtonHovered }, func(t *Tokens, c color.Color) { t.ButtonHovered = c }),
	}
	for i := range ansiTokenNames {
		specs = append(specs, ansiSpec(i))
	}
	return specs
}()

// Lookup resolves a token name to its colour. The second result is false for
// an unknown name, which is how strict theme files reject typos.
func (t Tokens) Lookup(name string) (color.Color, bool) {
	for _, s := range tokenSpecs {
		if s.name == name {
			return s.get(t), true
		}
	}
	return nil, false
}

// Set assigns a token by name and reports whether the name was known.
func (t *Tokens) Set(name string, c color.Color) bool {
	for _, s := range tokenSpecs {
		if s.name == name {
			s.set(t, c)
			return true
		}
	}
	return false
}

// Hex returns the canonical #rrggbb form of a token, or "" for an unknown
// name. It is the comparison form used by the token-table tests.
func (t Tokens) Hex(name string) string {
	c, ok := t.Lookup(name)
	if !ok {
		return ""
	}
	return hexOf(c)
}

// hexOf renders a colour as lower-case #rrggbb with round-to-nearest channel
// scaling, so a colour survives a round trip through color.Color unchanged.
func hexOf(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	ch := func(v uint32) uint8 { return uint8(float64(v)/65535*255 + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", ch(r), ch(g), ch(b))
}

// parseColorSpec accepts every spelling a theme file may use: #rgb/#rrggbb
// (alpha forms are accepted and flattened), an ANSI index 0-255, or a
// palette colour name.
func parseColorSpec(spec string) (color.Color, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return nil, errors.New("empty colour")
	}
	if strings.HasPrefix(s, "#") {
		return parseHexSpec(s)
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 255 {
			return nil, fmt.Errorf("ANSI colour index %d outside 0-255", n)
		}
		return lipgloss.Color(strconv.Itoa(n)), nil
	}
	if hex, ok := paletteHex(s); ok {
		return lipgloss.Color(hex), nil
	}
	return nil, fmt.Errorf("unknown colour %q (want #rgb, #rrggbb, an ANSI index 0-255, or a palette name)", spec)
}

// parseHexSpec validates a hex colour and normalises it to #rrggbb. Alpha is
// dropped: terminal cells are opaque, and blending transparency is a theme
// authoring concern, not a rendering one.
func parseHexSpec(s string) (color.Color, error) {
	h := strings.TrimPrefix(s, "#")
	for _, r := range h {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return nil, fmt.Errorf("malformed hex colour %q", s)
		}
	}
	switch len(h) {
	case 3, 4:
		// Expand the short form; a fourth digit is alpha and is dropped.
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6:
	case 8:
		h = h[:6]
	default:
		return nil, fmt.Errorf("malformed hex colour %q", s)
	}
	return lipgloss.Color("#" + strings.ToLower(h)), nil
}

// paletteColors is the palette palette as data: official name to hex. The
// default theme refers to these names, so the names are the contract and the
// hex values are an implementation detail of the palette.
var paletteColors = map[string]string{
	// Spectrum.
	"Cumin":    "#BF976F",
	"Tang":     "#FF985A",
	"Yam":      "#FFB587",
	"Paprika":  "#D36C64",
	"Bengal":   "#FF6E63",
	"Uni":      "#FF937D",
	"Sriracha": "#EB4268",
	"Coral":    "#FF577D",
	"Salmon":   "#FF7F90",
	"Chili":    "#E23080",
	"Cherry":   "#FF388B",
	"Tuna":     "#FF6DAA",
	"Macaron":  "#E940B0",
	"Pony":     "#FF4FBF",
	"Cheeky":   "#FF79D0",
	"Flamingo": "#F947E3",
	"Dolly":    "#FF60FF",
	"Blush":    "#FF84FF",
	"Urchin":   "#C337E0",
	"Mochi":    "#EB5DFF",
	"Lilac":    "#F379FF",
	"Prince":   "#9C35E1",
	"Violet":   "#C259FF",
	"Mauve":    "#D46EFF",
	"Grape":    "#7134DD",
	"Plum":     "#9953FF",
	"Orchid":   "#AD6EFF",
	"Jelly":    "#4A30D9",
	"Charple":  "#6B50FF",
	"Hazy":     "#8B75FF",
	"Ox":       "#3331B2",
	"Sapphire": "#4949FF",
	"Guppy":    "#7272FF",
	"Oceania":  "#2B55B3",
	"Thunder":  "#4776FF",
	"Anchovy":  "#719AFC",
	"Damson":   "#007AB8",
	"Malibu":   "#00A4FF",
	"Sardine":  "#4FBEFE",
	"Zinc":     "#10B1AE",
	"Turtle":   "#0ADCD9",
	"Lichen":   "#5CDFEA",
	"Guac":     "#12C78F",
	"Julep":    "#00FFB2",
	"Bok":      "#68FFD6",
	"Mustard":  "#F5EF34",
	"Citron":   "#E8FF27",
	"Zest":     "#E8FE96",
	"Butter":   "#FFFAF1",

	// Neutrals.
	"Pepper": "#201F26",
	"BBQ":    "#2D2C36",
	"Char":   "#3A3943",
	"Iron":   "#4D4C57",
	"Oyster": "#605F6B",
	"Squid":  "#858392",
	"Steam":  "#A2A0AD",
	"Smoke":  "#BFBCC8",
	"Steep":  "#D6D3DC",
	"Sash":   "#ECEBF0",
	"Salt":   "#F7F6FB",
	"Soda":   "#F1F0F5",

	// Charple ramp.
	"Darple": "#5B40EC",
	"Larple": "#7B62FF",

	// Diff additions.
	"Pickle":  "#00A475",
	"Gator":   "#18463D",
	"Spinach": "#1C3634",

	// Diff deletions.
	"Pom":   "#AB2454",
	"Steak": "#582238",
	"Toast": "#412130",

	// Secondaries outside the spectrum.
	"Ice":   "#00FFFC",
	"Pinxy": "#FF73FF",
	"Minty": "#00FFD7",

	// Appended spectrum shades.
	"Tahini":  "#A67F6C",
	"Tandoor": "#D9825E",
	"Bisque":  "#D99A86",

	// Appended diff tints.
	"Mojito": "#4EBEA0",
	"Dew":    "#A8DCD0",
	"Guava":  "#C36789",
	"Sakura": "#DFB3C6",
}

// paletteLower indexes the palette by lower-case name for case-insensitive
// lookups from theme files.
var paletteLower = func() map[string]string {
	m := make(map[string]string, len(paletteColors))
	for name, hex := range paletteColors {
		m[strings.ToLower(name)] = hex
	}
	return m
}()

// paletteHex resolves a palette name case-insensitively.
func paletteHex(name string) (string, bool) {
	hex, ok := paletteLower[strings.ToLower(strings.TrimSpace(name))]
	return hex, ok
}

// tone resolves a palette name in theme definitions. An unknown name is a
// programming error in a built-in theme, so it fails loudly at init.
func tone(name string) string {
	hex, ok := paletteHex(name)
	if !ok {
		panic("tui: unknown palette colour " + name)
	}
	return hex
}
