package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// permission mode config contract: default normal, strict validation,
// round-trip, and the deliberate absence of sysadmin.

func TestPermissionModeDefaultNormal(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Permissions.Mode != "normal" {
		t.Fatalf("default mode = %q, want normal", cfg.Permissions.Mode)
	}
	if len(cfg.Permissions.DangerousTools) != 0 {
		t.Fatalf("default dangerous_tools = %v, want empty", cfg.Permissions.DangerousTools)
	}
}

func TestPermissionModeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := "[permissions]\nmode = \"yolo\"\nauto_approve = [\"file_read\"]\ndangerous_tools = [\"mcp_deploy\", \"mcp_shell\"]\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Permissions.Mode != "yolo" {
		t.Fatalf("mode = %q, want yolo", cfg.Permissions.Mode)
	}
	if len(cfg.Permissions.DangerousTools) != 2 || cfg.Permissions.DangerousTools[0] != "mcp_deploy" {
		t.Fatalf("dangerous_tools = %v", cfg.Permissions.DangerousTools)
	}
}

func TestPermissionModeValidation(t *testing.T) {
	cases := []struct {
		value   string
		want    string
		wantErr bool
	}{
		{"", "normal", false},
		{"normal", "normal", false},
		{"yolo", "yolo", false},
		{"YOLO", "yolo", false}, // case-normalized
		{" yolo ", "yolo", false},
		{"sysadmin", "", true}, // omitted mode must be rejected, not silently accepted
		{"plan", "", true},
		{"yolo!", "", true},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		contents := "[permissions]\nmode = \"" + tc.value + "\"\n"
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFromPath(path)
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "permissions.mode") {
				t.Errorf("mode %q: err = %v, want permissions.mode validation error", tc.value, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("mode %q: unexpected error %v", tc.value, err)
			continue
		}
		if cfg.Permissions.Mode != tc.want {
			t.Errorf("mode %q normalized to %q, want %q", tc.value, cfg.Permissions.Mode, tc.want)
		}
	}
}
