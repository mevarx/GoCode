// User themes: JSON files in the config themes directory.
//
// A file is {"base": "<built-in or user theme>", "<token>": "<colour>",...}.
// The theme's name is the file stem, the document is strict (unknown tokens and
// malformed colours are errors, not silent no-ops), and a failed theme is
// skipped with a warning so one bad file cannot break startup.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mevarx/GoCode/internal/config"
)

// themesDir is where user themes live: <config dir>/themes.
func themesDir() string { return filepath.Join(config.ConfigDir(), "themes") }

// themeFile is one parsed user theme: its name (the file stem), the base it
// extends, and the token overrides it applies.
type themeFile struct {
	name      string
	base      string
	overrides map[string]string
}

// userThemeError keeps a failed theme's name next to its cause, so selecting
// that theme reports the real reason instead of a generic "unknown theme".
type userThemeError struct {
	name string
	err  error
}

func (e userThemeError) Error() string { return e.err.Error() }
func (e userThemeError) Unwrap() error { return e.err }

// userThemeSet is one pass over the themes directory: resolved themes plus the
// failures, indexed by name for lookups.
type userThemeSet struct {
	themes    []Theme
	byName    map[string]Theme
	errs      []error
	errByName map[string]error
}

// validThemeName keeps a theme name usable as both a config value and a file
// stem: letters, digits, dash, underscore and dot, but never a bare dot path.
func validThemeName(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// readThemeFile parses and validates one theme document. It validates colours
// here so a bad value is reported once, with its file and token, rather than
// at style-build time.
func readThemeFile(path string) (themeFile, error) {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !validThemeName(stem) {
		return themeFile{name: stem}, fmt.Errorf("theme file %s: invalid theme name %q (use letters, digits, '-', '_' or '.')", path, stem)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return themeFile{name: stem}, fmt.Errorf("theme file %s: %w", path, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return themeFile{name: stem}, fmt.Errorf("theme file %s: invalid JSON object: %w", path, err)
	}
	f := themeFile{name: stem, overrides: make(map[string]string, len(fields))}
	for key, raw := range fields {
		var value string
		if key == "base" {
			if err := json.Unmarshal(raw, &value); err != nil {
				return themeFile{name: stem}, fmt.Errorf("theme file %s: \"base\" must be a theme name", path)
			}
			f.base = strings.TrimSpace(value)
			continue
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return themeFile{name: stem}, fmt.Errorf("theme file %s: %q must be a colour string", path, key)
		}
		if _, ok := (Tokens{}).Lookup(key); !ok {
			return themeFile{name: stem}, fmt.Errorf("theme file %s: unknown token %q", path, key)
		}
		if _, err := parseColorSpec(value); err != nil {
			return themeFile{name: stem}, fmt.Errorf("theme file %s: token %q: %w", path, key, err)
		}
		f.overrides[key] = value
	}
	return f, nil
}

// saveUserThemeFile writes one user theme document. Keys and colours are
// validated before anything touches disk, so a failed save never leaves a
// half-written or invalid theme behind. The caller applies the theme to the
// UI only after this returns nil.
func saveUserThemeFile(name, base string, overrides map[string]string) error {
	if !validThemeName(name) {
		return fmt.Errorf("invalid theme name %q", name)
	}
	doc := make(map[string]string, len(overrides)+1)
	if base != "" {
		doc["base"] = base
	}
	for key, value := range overrides {
		if _, ok := (Tokens{}).Lookup(key); !ok {
			return fmt.Errorf("unknown token %q", key)
		}
		if _, err := parseColorSpec(value); err != nil {
			return fmt.Errorf("token %q: %w", key, err)
		}
		doc[key] = value
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	dir := themesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), append(data, '\n'), 0o644)
}

// loadUserThemeFiles reads every *.json theme in dir. A missing directory is
// not an error: most users have no custom themes.
func loadUserThemeFiles(dir string) ([]themeFile, []userThemeError) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []userThemeError{{err: fmt.Errorf("theme dir %s: %w", dir, err)}}
	}
	var files []themeFile
	var errs []userThemeError
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		f, err := readThemeFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			errs = append(errs, userThemeError{name: f.name, err: err})
			continue
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].name) < strings.ToLower(files[j].name) })
	return files, errs
}

// loadUserThemes is the simple view over the theme set.
func loadUserThemes() ([]Theme, []error) {
	set := loadUserThemeSet()
	return set.themes, set.errs
}

// loadUserThemeSet resolves every user theme against its base. Bases may be
// built-ins or other user themes; cycles and unknown bases are per-theme
// errors, so a broken file never hides the rest of the catalog.
func loadUserThemeSet() userThemeSet {
	files, fileErrs := loadUserThemeFiles(themesDir())
	set := userThemeSet{
		byName:    make(map[string]Theme, len(files)),
		errByName: make(map[string]error, len(fileErrs)),
	}
	for _, fe := range fileErrs {
		set.errs = append(set.errs, fe.err)
		if fe.name != "" {
			set.errByName[strings.ToLower(fe.name)] = fe.err
		}
	}
	if len(files) == 0 {
		return set
	}

	filesByName := make(map[string]themeFile, len(files))
	keys := make([]string, 0, len(files))
	for _, f := range files {
		key := strings.ToLower(f.name)
		filesByName[key] = f
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var resolve func(key string, chain []string) (Theme, error)
	resolve = func(key string, chain []string) (Theme, error) {
		if th, ok := set.byName[key]; ok {
			return th, nil
		}
		if th, ok := builtinThemesByKey[key]; ok {
			return th, nil
		}
		for _, seen := range chain {
			if seen == key {
				return Theme{}, fmt.Errorf("base cycle: %s", strings.Join(append(chain, key), " -> "))
			}
		}
		f, ok := filesByName[key]
		if !ok {
			return Theme{}, fmt.Errorf("unknown base theme %q", key)
		}
		baseKey := normalizeThemeName(f.base)
		if baseKey == "" {
			baseKey = DefaultThemeName
		}
		base, err := resolve(baseKey, append(append([]string{}, chain...), key))
		if err != nil {
			return Theme{}, fmt.Errorf("theme %q: %w", f.name, err)
		}
		th, err := deriveUserTheme(base, f.name, f.overrides)
		if err != nil {
			return Theme{}, fmt.Errorf("theme %q: %w", f.name, err)
		}
		set.byName[key] = th
		return th, nil
	}

	for _, key := range keys {
		th, err := resolve(key, nil)
		if err != nil {
			set.errs = append(set.errs, err)
			if _, ok := set.errByName[key]; !ok {
				set.errByName[key] = err
			}
			continue
		}
		set.themes = append(set.themes, th)
	}
	sort.Slice(set.themes, func(i, j int) bool {
		return strings.ToLower(set.themes[i].Name) < strings.ToLower(set.themes[j].Name)
	})
	return set
}

// deriveUserTheme applies token overrides to a base theme. Diff bands follow a
// recoloured diff ink or app background; an unrelated override keeps the base
// theme's fitted bands.
func deriveUserTheme(base Theme, name string, overrides map[string]string) (Theme, error) {
	t := base.Tokens
	set := make(map[string]bool, len(overrides))
	for token, spec := range overrides {
		c, err := parseColorSpec(spec)
		if err != nil {
			return Theme{}, fmt.Errorf("token %q: %w", token, err)
		}
		if !t.Set(token, c) {
			return Theme{}, fmt.Errorf("unknown token %q", token)
		}
		set[token] = true
	}
	if !set["diff_insert_code_bg"] && (set["diff_insert_fg"] || set["bg_base"]) {
		t.DiffInsertCodeBg = diffTint(t.DiffInsertFg, t.BgBase, diffInsertCodeFraction)
	}
	if !set["diff_insert_gutter_bg"] && (set["diff_insert_fg"] || set["bg_base"]) {
		t.DiffInsertGutterBg = diffTint(t.DiffInsertFg, t.BgBase, diffInsertGutterFraction)
	}
	if !set["diff_delete_code_bg"] && (set["diff_delete_fg"] || set["bg_base"]) {
		t.DiffDeleteCodeBg = diffTint(t.DiffDeleteFg, t.BgBase, diffDeleteCodeFraction)
	}
	if !set["diff_delete_gutter_bg"] && (set["diff_delete_fg"] || set["bg_base"]) {
		t.DiffDeleteGutterBg = diffTint(t.DiffDeleteFg, t.BgBase, diffDeleteGutterFraction)
	}
	return Theme{
		Name:      name,
		Label:     name,
		Light:     base.Light,
		Tokens:    t,
		Overrides: buildOverrides(t, base.patch),
		patch:     base.patch,
	}, nil
}
