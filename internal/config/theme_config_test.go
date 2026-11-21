package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfig_TUIThemeDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TUI.ActiveTheme != TUIDefaultTheme {
		t.Errorf("default active_theme = %q, want %q", cfg.TUI.ActiveTheme, TUIDefaultTheme)
	}
	if cfg.TUI.Transparent {
		t.Error("transparent must default to false")
	}
}

func TestConfig_TUIThemeKeysRoundTrip(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	contents := "[tui]\nactive_theme = \"rose-pine-moon\"\ntransparent = true\n"
	if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.TUI.ActiveTheme != "rose-pine-moon" {
		t.Errorf("active_theme = %q, want rose-pine-moon", loaded.TUI.ActiveTheme)
	}
	if !loaded.TUI.Transparent {
		t.Error("transparent = true was not applied")
	}

	outFile := filepath.Join(t.TempDir(), "roundtrip.toml")
	if err := SaveToPath(loaded, outFile); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	again, err := LoadFromPath(outFile)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if again.TUI.ActiveTheme != "rose-pine-moon" || !again.TUI.Transparent {
		t.Fatalf("theme keys did not round-trip: %+v", again.TUI)
	}
}

func TestConfig_TUIThemeDefaultsSurviveUnrelatedFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte("[provider]\ndefault = \"openai\"\n"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.TUI.ActiveTheme != TUIDefaultTheme {
		t.Errorf("default active_theme lost: %q", loaded.TUI.ActiveTheme)
	}
	if loaded.TUI.Transparent {
		t.Error("transparent default lost")
	}
}

func TestConfig_EmptyThemeNameUsesDefault(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte("[tui]\nactive_theme = \"  \"\n"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.TUI.ActiveTheme != TUIDefaultTheme {
		t.Errorf("active_theme = %q, want the default", loaded.TUI.ActiveTheme)
	}
}

func TestConfig_InvalidThemeNameRejected(t *testing.T) {
	for _, name := range []string{"../evil", "a/b", "has space", "×"} {
		tmpFile := filepath.Join(t.TempDir(), "config.toml")
		contents := "[tui]\nactive_theme = \"" + name + "\"\n"
		if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		_, err := LoadFromPath(tmpFile)
		if err == nil {
			t.Errorf("active_theme %q was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "not a valid theme name") {
			t.Errorf("active_theme %q error = %v, want a name-validation error", name, err)
		}
	}
}

func TestConfig_TUIThemeUnknownKeyRejected(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte("[tui]\nactive_them = \"keanu\"\n"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	_, err := LoadFromPath(tmpFile)
	if err == nil {
		t.Fatal("misspelled theme key must be rejected")
	}
	if !strings.Contains(err.Error(), "unknown keys") || !strings.Contains(err.Error(), "active_them") {
		t.Fatalf("error does not name the unknown key: %v", err)
	}
}
