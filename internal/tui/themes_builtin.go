// Built-in theme definitions, expressed as data: token name to colour spec.
// Values are design constants; the default theme refers to named palette
// colours so the names remain the contract.
package tui

// themeDef is a built-in theme expressed as data. Tokens left out are filled
// by the derivation rules (diff bands, diff inks, destructive_subtle).
type themeDef struct {
	name    string
	label   string
	light   bool
	palette map[string]string
	patch   overridePatch
}

// overridePatch carries optional built-in exceptions as colour specs. The zero
// value means "use the generic derivation".
type overridePatch struct {
	inlineCodeFg string
	inlineCodeBg string
	link         string
	image        string
	subduedIcon  string

	promptBadgeFg      string
	promptBadgeBg      string
	promptDotsFocused  string
	promptDotsBlurred  string
	shellBorderFocused string
	shellBorderBlurred string
	shellPrompt        string

	statusToastBg  string
	statusToastInk string
	warnFill       string
	yoloBody       string
	yoloInk        string
	toolName       string
	surfaceText    string

	questionBadgeInk string
	planBadgeInk     string
	h1Ink            string

	syntaxCommentPreproc   string
	syntaxKeywordReserved  string
	syntaxKeywordNamespace string
	syntaxKeywordType      string
	syntaxOperator         string
	syntaxNameTag          string
	syntaxNameAttribute    string
	syntaxNameClass        string
	syntaxLiteralString    string
	syntaxPunctuation      string
}

// defaultThemeDef is the default palette, expressed with palette names so
// the names stay the contract. The four diff backgrounds are the fitted
// constants the design was tuned to (the generic CIELAB derivation lands a few
// steps away, which is why the default pins them).
var defaultThemeDef = themeDef{
	name:  DefaultThemeName,
	label: "GoCode Dark",
	palette: map[string]string{
		"primary":   tone("Charple"),
		"secondary": tone("Dolly"),
		"accent":    tone("Bok"),
		"keyword":   tone("Blush"),

		"fg_base":        tone("Sash"),
		"fg_subtle":      tone("Smoke"),
		"fg_more_subtle": tone("Squid"),
		"fg_most_subtle": tone("Oyster"),

		"bg_base":          tone("Pepper"),
		"bg_most_visible":  tone("Iron"),
		"bg_less_visible":  tone("Char"),
		"bg_least_visible": tone("BBQ"),

		"on_primary": tone("Butter"),
		"separator":  tone("Char"),

		"destructive":         tone("Coral"),
		"destructive_subtle":  tone("Salmon"),
		"error":               tone("Sriracha"),
		"warning":             tone("Mustard"),
		"warning_subtle":      tone("Zest"),
		"attention":           tone("Tang"),
		"busy":                tone("Citron"),
		"info":                tone("Malibu"),
		"info_more_subtle":    tone("Sardine"),
		"info_most_subtle":    tone("Damson"),
		"success":             tone("Julep"),
		"success_more_subtle": tone("Bok"),
		"success_most_subtle": tone("Guac"),

		"diff_insert_fg":        "#629657",
		"diff_insert_code_bg":   "#303A30",
		"diff_insert_gutter_bg": "#293229",
		"diff_delete_fg":        "#A45C59",
		"diff_delete_code_bg":   "#3A3030",
		"diff_delete_gutter_bg": "#332929",

		"yolo":             tone("Zest"),
		"plan":             tone("Charple"),
		"plan_more_subtle": tone("Hazy"),

		"button":          tone("Dolly"),
		"button_subtle":   tone("Char"),
		"button_inactive": tone("Iron"),
		"button_hovered":  tone("Oyster"),

		"ansi_black":          tone("BBQ"),
		"ansi_red":            tone("Coral"),
		"ansi_green":          tone("Guac"),
		"ansi_yellow":         tone("Mustard"),
		"ansi_blue":           tone("Charple"),
		"ansi_magenta":        tone("Dolly"),
		"ansi_cyan":           tone("Malibu"),
		"ansi_white":          tone("Smoke"),
		"ansi_bright_black":   tone("Iron"),
		"ansi_bright_red":     tone("Tuna"),
		"ansi_bright_green":   tone("Julep"),
		"ansi_bright_yellow":  tone("Zest"),
		"ansi_bright_blue":    tone("Guppy"),
		"ansi_bright_magenta": tone("Blush"),
		"ansi_bright_cyan":    tone("Sardine"),
		"ansi_bright_white":   tone("Salt"),
	},
	patch: overridePatch{
		link:        tone("Zinc"),
		image:       tone("Cheeky"),
		subduedIcon: tone("Violet"),

		promptBadgeFg:      tone("Salt"),
		promptBadgeBg:      tone("Hazy"),
		promptDotsFocused:  tone("Hazy"),
		promptDotsBlurred:  tone("Larple"),
		shellBorderFocused: tone("Charple"),
		shellBorderBlurred: tone("Iron"),
		shellPrompt:        tone("Hazy"),

		syntaxCommentPreproc:   tone("Bengal"),
		syntaxKeywordReserved:  tone("Pony"),
		syntaxKeywordNamespace: tone("Pony"),
		syntaxKeywordType:      tone("Guppy"),
		syntaxOperator:         tone("Salmon"),
		syntaxNameTag:          tone("Mauve"),
		syntaxNameAttribute:    tone("Hazy"),
		syntaxNameClass:        tone("Salt"),
		syntaxLiteralString:    tone("Cumin"),
	},
}

// gruvboxThemeDef is the morhetz gruvbox dark palette.
var gruvboxThemeDef = themeDef{
	name:  "gruvbox-dark",
	label: "Gruvbox Dark",
	palette: map[string]string{
		"primary":   "#fabd2f",
		"secondary": "#d3869b",
		"accent":    "#b8bb26",
		"keyword":   "#fe8019",

		"fg_base":        "#ebdbb2",
		"fg_subtle":      "#bdae93",
		"fg_more_subtle": "#a89984",
		"fg_most_subtle": "#928374",

		"bg_base":          "#282828",
		"bg_most_visible":  "#665c54",
		"bg_less_visible":  "#504945",
		"bg_least_visible": "#3c3836",

		"on_primary": "#282828",
		"separator":  "#504945",

		"destructive":         "#fb4934",
		"destructive_subtle":  "#fe8019",
		"error":               "#cc241d",
		"warning":             "#d79921",
		"warning_subtle":      "#fabd2f",
		"attention":           "#fe8019",
		"busy":                "#fabd2f",
		"info":                "#83a598",
		"info_more_subtle":    "#83a598",
		"info_most_subtle":    "#458588",
		"success":             "#b8bb26",
		"success_more_subtle": "#b8bb26",
		"success_most_subtle": "#8ec07c",

		"yolo":             "#fabd2f",
		"plan":             "#d3869b",
		"plan_more_subtle": "#665c54",

		"button":          "#d3869b",
		"button_subtle":   "#504945",
		"button_inactive": "#665c54",
		"button_hovered":  "#928374",

		"ansi_black":          "#282828",
		"ansi_red":            "#cc241d",
		"ansi_green":          "#98971a",
		"ansi_yellow":         "#d79921",
		"ansi_blue":           "#458588",
		"ansi_magenta":        "#b16286",
		"ansi_cyan":           "#689d6a",
		"ansi_white":          "#a89984",
		"ansi_bright_black":   "#928374",
		"ansi_bright_red":     "#fb4934",
		"ansi_bright_green":   "#b8bb26",
		"ansi_bright_yellow":  "#fabd2f",
		"ansi_bright_blue":    "#83a598",
		"ansi_bright_magenta": "#d3869b",
		"ansi_bright_cyan":    "#8ec07c",
		"ansi_bright_white":   "#ebdbb2",
	},
	patch: overridePatch{
		inlineCodeFg: "#fe8019",
		inlineCodeBg: "#282828",
	},
}

// keanuThemeDef is black plus phosphor green.
var keanuThemeDef = themeDef{
	name:  "keanu",
	label: "Keanu",
	palette: map[string]string{
		"primary":   "#00ff41",
		"secondary": "#7dffa6",
		"accent":    "#00d78f",
		"keyword":   "#c8ff4d",

		"fg_base":        "#00ff41",
		"fg_subtle":      "#00d447",
		"fg_more_subtle": "#00a838",
		"fg_most_subtle": "#008a30",

		"bg_base":          "#000000",
		"bg_most_visible":  "#24572f",
		"bg_less_visible":  "#183c22",
		"bg_least_visible": "#0e2415",

		"on_primary": "#001507",
		"separator":  "#183c22",

		"destructive":         "#ff5252",
		"destructive_subtle":  "#ff7a7a",
		"error":               "#d43d3d",
		"warning":             "#b8d43d",
		"warning_subtle":      "#d7f75b",
		"attention":           "#ff9f43",
		"busy":                "#c8ff4d",
		"info":                "#00d9c3",
		"info_more_subtle":    "#00b3a1",
		"info_most_subtle":    "#008f7f",
		"success":             "#00ff41",
		"success_more_subtle": "#00c443",
		"success_most_subtle": "#009637",

		"yolo":             "#c8ff4d",
		"plan":             "#4dffb8",
		"plan_more_subtle": "#0d332b",

		"button":          "#00ff41",
		"button_subtle":   "#183c22",
		"button_inactive": "#24572f",
		"button_hovered":  "#00d447",

		"ansi_black":          "#0e2415",
		"ansi_red":            "#ff5252",
		"ansi_green":          "#00d447",
		"ansi_yellow":         "#d7f75b",
		"ansi_blue":           "#00b8d9",
		"ansi_magenta":        "#b98cff",
		"ansi_cyan":           "#00e0c3",
		"ansi_white":          "#a8d9b8",
		"ansi_bright_black":   "#24572f",
		"ansi_bright_red":     "#ff7a7a",
		"ansi_bright_green":   "#00ff41",
		"ansi_bright_yellow":  "#eaff66",
		"ansi_bright_blue":    "#33cfff",
		"ansi_bright_magenta": "#d1a6ff",
		"ansi_bright_cyan":    "#66ffe0",
		"ansi_bright_white":   "#e6ffe6",
	},
	patch: overridePatch{
		inlineCodeFg:      "#c8ff8f",
		inlineCodeBg:      "#183c22",
		questionBadgeInk:  "#001507",
		planBadgeInk:      "#001507",
		h1Ink:             "#001507",
		syntaxPunctuation: "#00d447",
	},
}

// rosePineThemeDef is Rosé Pine (dark).
var rosePineThemeDef = themeDef{
	name:  "rose-pine",
	label: "Rosé Pine",
	palette: map[string]string{
		"primary":   "#c4a7e7",
		"secondary": "#ebbcba",
		"accent":    "#9ccfd8",
		"keyword":   "#eb6f92",

		"fg_base":        "#e0def4",
		"fg_subtle":      "#908caa",
		"fg_more_subtle": "#6e6a86",
		"fg_most_subtle": "#524f67",

		"bg_base":          "#191724",
		"bg_most_visible":  "#3f3d4c",
		"bg_less_visible":  "#2f2d3b",
		"bg_least_visible": "#23212e",

		"on_primary": "#1d1923",
		"separator":  "#2f2d3b",

		"destructive":         "#eb6f92",
		"destructive_subtle":  "#ebbcba",
		"error":               "#eb6f92",
		"warning":             "#f6c177",
		"warning_subtle":      "#bf9662",
		"attention":           "#f6c177",
		"busy":                "#f6c177",
		"info":                "#31748f",
		"info_more_subtle":    "#4b6a7f",
		"info_most_subtle":    "#46596d",
		"success":             "#31748f",
		"success_more_subtle": "#4c6e84",
		"success_most_subtle": "#485f72",

		"diff_insert_fg": "#31748f",
		"diff_delete_fg": "#eb6f92",

		"yolo":             "#f6c177",
		"plan":             "#c4a7e7",
		"plan_more_subtle": "#3f3d4c",

		"button":          "#ebbcba",
		"button_subtle":   "#2f2d3b",
		"button_inactive": "#3f3d4c",
		"button_hovered":  "#e8c6ca",

		"ansi_black":          "#191724",
		"ansi_red":            "#eb6f92",
		"ansi_green":          "#31748f",
		"ansi_yellow":         "#f6c177",
		"ansi_blue":           "#31748f",
		"ansi_magenta":        "#ebbcba",
		"ansi_cyan":           "#9ccfd8",
		"ansi_white":          "#908caa",
		"ansi_bright_black":   "#524f67",
		"ansi_bright_red":     "#f092ac",
		"ansi_bright_green":   "#6295aa",
		"ansi_bright_yellow":  "#f8d098",
		"ansi_bright_blue":    "#6295aa",
		"ansi_bright_magenta": "#f0cccb",
		"ansi_bright_cyan":    "#b4dbe1",
		"ansi_bright_white":   "#e0def4",
	},
}

// rosePineMoonThemeDef is Rosé Pine Moon (dark).
var rosePineMoonThemeDef = themeDef{
	name:  "rose-pine-moon",
	label: "Rosé Pine Moon",
	palette: map[string]string{
		"primary":   "#c4a7e7",
		"secondary": "#ea9a97",
		"accent":    "#9ccfd8",
		"keyword":   "#eb6f92",

		"fg_base":        "#e0def4",
		"fg_subtle":      "#908caa",
		"fg_more_subtle": "#6e6a86",
		"fg_most_subtle": "#56526e",

		"bg_base":          "#232136",
		"bg_most_visible":  "#47455a",
		"bg_less_visible":  "#38364b",
		"bg_least_visible": "#2c2a40",

		"on_primary": "#1d1923",
		"separator":  "#38364b",

		"destructive":         "#eb6f92",
		"destructive_subtle":  "#ebbcba",
		"error":               "#eb6f92",
		"warning":             "#f6c177",
		"warning_subtle":      "#c19967",
		"attention":           "#f6c177",
		"busy":                "#f6c177",
		"info":                "#3e8fb0",
		"info_more_subtle":    "#547a96",
		"info_most_subtle":    "#4f677f",
		"success":             "#3e8fb0",
		"success_more_subtle": "#467490",
		"success_most_subtle": "#516d86",

		"diff_insert_fg": "#3e8fb0",
		"diff_delete_fg": "#eb6f92",

		"yolo":             "#f6c177",
		"plan":             "#c4a7e7",
		"plan_more_subtle": "#47455a",

		"button":          "#ea9a97",
		"button_subtle":   "#38364b",
		"button_inactive": "#47455a",
		"button_hovered":  "#e7adb1",

		"ansi_black":          "#232136",
		"ansi_red":            "#eb6f92",
		"ansi_green":          "#3e8fb0",
		"ansi_yellow":         "#f6c177",
		"ansi_blue":           "#3e8fb0",
		"ansi_magenta":        "#ea9a97",
		"ansi_cyan":           "#9ccfd8",
		"ansi_white":          "#908caa",
		"ansi_bright_black":   "#56526e",
		"ansi_bright_red":     "#f092ac",
		"ansi_bright_green":   "#6caac3",
		"ansi_bright_yellow":  "#f8d098",
		"ansi_bright_blue":    "#6caac3",
		"ansi_bright_magenta": "#efb2b0",
		"ansi_bright_cyan":    "#b4dbe1",
		"ansi_bright_white":   "#e0def4",
	},
}

// rosePineDawnThemeDef is Rosé Pine Dawn — the light theme.
var rosePineDawnThemeDef = themeDef{
	name:  LightThemeName,
	label: "Rosé Pine Dawn",
	light: true,
	palette: map[string]string{
		"primary":   "#907aa9",
		"secondary": "#d7827e",
		"accent":    "#56949f",
		"keyword":   "#b4637a",

		"fg_base":        "#464261",
		"fg_subtle":      "#575279",
		"fg_more_subtle": "#797593",
		"fg_most_subtle": "#9893a5",

		"bg_base":          "#faf4ed",
		"bg_most_visible":  "#cecacd",
		"bg_less_visible":  "#dfdad9",
		"bg_least_visible": "#f2e9e1",

		"on_primary": "#161219",
		"separator":  "#cecacd",

		"destructive":         "#b4637a",
		"destructive_subtle":  "#d7827e",
		"error":               "#b4637a",
		"warning":             "#ea9d34",
		"warning_subtle":      "#9c6a2b",
		"attention":           "#9c6a2b",
		"busy":                "#ea9d34",
		"info":                "#286983",
		"info_more_subtle":    "#56949f",
		"info_most_subtle":    "#56949f",
		"success":             "#286983",
		"success_more_subtle": "#56949f",
		"success_most_subtle": "#56949f",

		"diff_insert_fg": "#286983",
		"diff_delete_fg": "#b4637a",

		"yolo":             "#ea9d34",
		"plan":             "#907aa9",
		"plan_more_subtle": "#cecacd",

		"button":          "#d7827e",
		"button_subtle":   "#dfdad9",
		"button_inactive": "#cecacd",
		"button_hovered":  "#b3757d",

		"ansi_black":          "#faf4ed",
		"ansi_red":            "#b4637a",
		"ansi_green":          "#286983",
		"ansi_yellow":         "#9c6a2b",
		"ansi_blue":           "#286983",
		"ansi_magenta":        "#907aa9",
		"ansi_cyan":           "#56949f",
		"ansi_white":          "#797593",
		"ansi_bright_black":   "#9893a5",
		"ansi_bright_red":     "#894b5d",
		"ansi_bright_green":   "#1e5064",
		"ansi_bright_yellow":  "#8a5f24",
		"ansi_bright_blue":    "#1e5064",
		"ansi_bright_magenta": "#7d6598",
		"ansi_bright_cyan":    "#417079",
		"ansi_bright_white":   "#464261",
	},
	patch: overridePatch{
		statusToastBg:    "#8fa2b0",
		statusToastInk:   "#575279",
		warnFill:         "#a7856d",
		yoloBody:         "#f6c177",
		yoloInk:          "#575279",
		toolName:         "#56949f",
		surfaceText:      "#797593",
		questionBadgeInk: "#161219",
	},
}

// builtinThemeDefs is the canonical order: default first, then light order.
var builtinThemeDefs = []themeDef{
	defaultThemeDef,
	gruvboxThemeDef,
	keanuThemeDef,
	rosePineThemeDef,
	rosePineMoonThemeDef,
	rosePineDawnThemeDef,
}
