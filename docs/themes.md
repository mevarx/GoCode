# Themes

GoCode's TUI is painted entirely from semantic colour tokens. A theme is a
fully resolved palette — no colour is derived at render time — and switching
themes repaints every style without touching a call site. This page documents
the six built-in themes, the token model, user themes, and the in-app picker
and palette editor.

The implementation lives in `internal/tui/theme.go`, `palette.go`,
`themes_builtin.go`, `derive.go`, `user_theme.go`, `styles.go` and
`theme_dialog.go`. The named colour map in `palette.go` is plain colour data
(names to hex).

- [Built-in themes](#built-in-themes)
- [The token model](#the-token-model)
- [Colour values](#colour-values)
- [Derivation rules](#derivation-rules)
- [User themes](#user-themes)
- [Selecting a theme](#selecting-a-theme)
- [Theme picker (Ctrl+T)](#theme-picker-ctrlt)
- [Palette editor (Ctrl+E)](#palette-editor-ctrle)
- [How a switch propagates](#how-a-switch-propagates)

## Built-in themes

Six themes ship in `internal/tui/themes_builtin.go`. Names match
case-insensitively; the deprecated short alias `gocode` resolves to
`gocode-dark`.

| Name | Label | Polarity |
| :-- | :-- | :-- |
| `gocode-dark` | GoCode Dark | dark (default) |
| `gruvbox-dark` | Gruvbox Dark | dark |
| `keanu` | Keanu | dark |
| `rose-pine` | Rosé Pine | dark |
| `rose-pine-moon` | Rosé Pine Moon | dark |
| `rose-pine-dawn` | Rosé Pine Dawn | light |

Light mode is a theme, not an adaptive colour switch: the UI never asks the
terminal for its background colour. `rose-pine-dawn` is the built-in light
theme (`tui.LightThemeName`); `GOCODE_THEME=light` selects it.

Every built-in defines all 56 token keys (a nil role would render as "no
colour", so the token-table tests assert each one hex-exact).

## The token model

`Tokens` (`internal/tui/palette.go`) is one fully resolved palette: **40
semantic roles plus a 16-slot ANSI remap** for raw terminal output — 56 keys
total, in the canonical order of `tokenSpecs`.

| Group | Keys |
| :-- | :-- |
| Brand | `primary`, `secondary`, `accent`, `keyword` |
| Text ramp | `fg_base`, `fg_subtle`, `fg_more_subtle`, `fg_most_subtle` |
| Surfaces | `bg_base`, `bg_most_visible`, `bg_less_visible`, `bg_least_visible`, `on_primary`, `separator` |
| Status | `destructive`, `destructive_subtle`, `error`, `warning`, `warning_subtle`, `attention`, `busy`, `info`, `info_more_subtle`, `info_most_subtle`, `success`, `success_more_subtle`, `success_most_subtle` |
| Diffs | `diff_insert_fg`, `diff_insert_code_bg`, `diff_insert_gutter_bg`, `diff_delete_fg`, `diff_delete_code_bg`, `diff_delete_gutter_bg` |
| Mode chrome | `yolo`, `plan`, `plan_more_subtle` |
| Buttons | `button`, `button_subtle`, `button_inactive`, `button_hovered` |
| ANSI remap | `ansi_black`, `ansi_red`, `ansi_green`, `ansi_yellow`, `ansi_blue`, `ansi_magenta`, `ansi_cyan`, `ansi_white`, `ansi_bright_black`, `ansi_bright_red`, `ansi_bright_green`, `ansi_bright_yellow`, `ansi_bright_blue`, `ansi_bright_magenta`, `ansi_bright_cyan`, `ansi_bright_white` |

`Tokens.Lookup` / `Tokens.Set` resolve a key by name; an unknown name reports
false, which is how strict theme files reject typos. `Tokens.Hex` renders the
canonical lower-case `#rrggbb` comparison form.

**Overrides.** A few roles live outside the palette (`Overrides` in
`theme.go`): inline-code colours, markdown pins (`Link`, `Image`,
`SubduedIcon`), prompt chrome (badge, dots, shell borders/prompt), toast and
banner pins, badge inks, and the `Syntax` table. They exist so a built-in can
keep an authored exception without widening the palette every theme must
define. Most are derived from tokens; a theme may pin them.

**Syntax.** `Syntax` is the single chroma role table. Markdown fenced blocks,
numbered file views and diffs all read it, so highlighting can never disagree
with itself. Its generic mapping (`buildSyntax`) derives every role from
tokens — e.g. keywords from `info`/`keyword`, strings from `warning_subtle`,
numbers from `success`, inserted lines from `success_most_subtle`, deleted
lines from `destructive`.

## Colour values

A token value (`parseColorSpec`, `palette.go`) may be written as:

- `#rgb` or `#rrggbb` hex. Alpha forms (`#rgba`, `#rrggbbaa`) are accepted and
  flattened — terminal cells are opaque, so alpha is dropped.
- An ANSI colour index `0`–`255`.
- A palette colour name, case-insensitive (`Charple`, `Julep`, `Pepper`,
  `Sakura`, … — the full map is `paletteColors` in `palette.go`). The
  default theme refers to these names, so the names are the contract and the
  hex values are palette data.

Anything else is an error, reported with the file and token when a user theme
loads.

## Derivation rules

`derive.go` and `deriveTokens`/`buildOverrides` in `theme.go` fill the roles a
theme may leave unauthored. The rules are design constants, not preferences:

- **Diff ink fallbacks.** `diff_insert_fg` defaults to `success`;
  `diff_delete_fg` defaults to `destructive`.
- **Diff bands.** Each band is a CIELAB tint of its diff ink toward the app
  background (`bg_base`). Code bands lean harder than their line-number
  gutters: insert code `0.20`, insert gutter `0.13`, delete code `0.15`,
  delete gutter `0.08` (the fraction is how far the band leans toward the
  background). A pinned band — the default theme authors fitted bands — is
  never overwritten.
- **`destructive_subtle`** falls back to `destructive` when unauthored.
- **Badge ink contrast.** Fill-based badges (YOLO, question, plan, H1) keep
  their authored ink unless it would be illegible on the fill; then the
  contrast rule (`badgeInk`) substitutes a readable ink.
- **Inline-code sentinels.** Inline code is wrapped in the no-break-space +
  variation-selector pair `U+00A0 U+FE0E` so terminals render it as text, not
  emoji, and copy/paste keeps its boundaries.

## User themes

User themes are JSON files in the config **themes directory**:
`<config dir>/themes/` (`~/.config/gocode/themes/` on Linux/macOS,
`%APPDATA%\gocode\themes\` on Windows — see
[configuration.md](configuration.md#platform-paths)).

A file is a flat object: `base` names the theme to extend, and every other
key is a token override.

```json
{
  "base": "gruvbox-dark",
  "primary": "#d79921",
  "accent": "Julep",
  "bg_base": "#1d2021"
}
```

Rules (`internal/tui/user_theme.go`):

- The theme's **name is the file stem** (`my-theme.json` → `my-theme`). Stems
  must be valid theme names: letters, digits, `-`, `_`, `.`, at most 64
  characters, never `.` or `..`.
- The document is **strict**: unknown tokens, non-string values and malformed
  colours are errors, not silent no-ops.
- `base` may be a built-in or another user theme; an empty/missing base means
  `gocode-dark`. Base cycles and unknown bases are per-theme errors.
- A failed theme is **skipped with a warning** — one bad file cannot break
  startup or hide the rest of the catalog. Selecting a broken theme by name
  reports its real cause instead of a generic "unknown theme".
- Overrides apply onto the base's resolved tokens. Diff bands are re-tinted
  when you recolour a diff ink or `bg_base` (unless you pin the band
  yourself); an unrelated override keeps the base theme's fitted bands.
- The light/dark flag is inherited from the base; the label equals the name.
- Files are read case-insensitively by extension (`.json`) and listed sorted
  by name after the built-ins.

## Selecting a theme

Three ways, in precedence order (`ApplyThemeSettings`, `theme.go`):

1. **`GOCODE_THEME`** — `light` selects `rose-pine-dawn`, `dark` selects
   `gocode-dark`. Any other value is ignored (the variable only forces
   a background polarity).
2. **`[tui] active_theme`** in `config.toml` — default `gocode-dark`.
   The name is validated at config load (same shape rules as file stems);
   matching is case-insensitive. An unknown name never blocks startup: it
   logs a warning and falls back to the default theme.
3. The **default** (`gocode-dark`) when nothing is configured.

`[tui] transparent = true` skips painting the app background so the
terminal's own background shows through; `NO_COLOR` and `TERM=dumb` leave the
terminal background alone as well. See
[configuration.md](configuration.md#tui) for both keys.

The theme picker's Enter and the palette editor's save also write
`active_theme` back to `config.toml`, so an in-app choice survives restarts.

## Theme picker (Ctrl+T)

`Ctrl+T` opens the theme picker (`theme_dialog.go`), a modal list of built-ins
(sorted) followed by user themes, each marked `built-in` or `user`:

| Key | Action |
| :-- | :-- |
| `↑` / `↓` | Move the cursor and **preview the theme live** |
| type | Filter the list |
| `Enter` | Save: persist `active_theme` to `config.toml`, toast the result |
| `Esc` | Revert to the theme that was active when the picker opened |
| `Ctrl+E` | Open the palette editor on the selected theme |

A failed preview (broken user file) is ignored mid-navigation so the UI never
blanks. A failed save keeps the previewed theme but reports the error as a
toast and an error line. Like every overlay, the picker yields to the
approval modal: pushing approval drops it so approval keys can never go
nowhere.

## Palette editor (Ctrl+E)

From the picker, `Ctrl+E` opens the palette editor on the selected theme. It
edits **one token at a time** over the canonical 56-key order, showing a
sliding window of ten fields:

| Key | Action |
| :-- | :-- |
| `Tab` / `Shift+Tab` | Commit the current field and move to the next/previous |
| typing | Live preview: a valid colour updates the value and repaints; an invalid one keeps the old value and shows the reason |
| `Enter` / `Ctrl+S` | Save the theme file, then commit the UI to it |
| `Esc` | Revert to the theme that was active when the editor opened |

- Editing a **built-in** saves a user override named `<name>-custom`; the
  built-in itself is never modified.
- Only tokens that **differ from the base** are written, so the user file
  stays a small, readable override document (`{"base": …, …}`).
- **File first, colours second**: the theme file is validated and written to
  disk before the UI commits to it, so a failed save never leaves colours
  applied that did not reach disk.

## How a switch propagates

`ActivateTheme` resolves the name and installs the theme on the update loop
only (no lock needed). Installation:

1. repaints the shared style bag — `rebuildStyles()` in `styles.go` is the
   one place styles are constructed, and every value derives from the active
   theme's tokens (there is no hex in that file);
2. updates the capability probe (`DarkBackground`) from the theme's polarity;
3. bumps `themeGeneration`, which keys every markdown renderer/cache entry,
   so a switch invalidates all cached renders without walking them.

The render cache's width-epoch contract is untouched: cached heights survive
a theme switch; only rendered strings are rebuilt.