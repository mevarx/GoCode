package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/provider"
)

// isolateConfigDir points every platform-specific config path at a temp
// directory so a test that reads or writes the saved Copilot token cannot see
// — or clobber — the developer's real one.
//
// config.ConfigDir() uses APPDATA on Windows and XDG_CONFIG_HOME / $HOME
// elsewhere, so all of them have to be redirected. Setting only APPDATA left
// macOS and Linux tests writing into ~/.config/gocode.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	return dir
}

// A user who completed `gocode auth copilot` has a token saved on disk.
// `doctor` must not tell them they have none.
//
// This was a real regression: doctor checked only the environment variable
// while the provider also reads the saved file, so the health check disagreed
// with the provider that actually does the work.
func TestDoctorReportsCopilotTokenFromSavedFile(t *testing.T) {
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "")
	isolateConfigDir(t)

	path := config.CopilotTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("ghu_saved_by_device_flow"), 0o600); err != nil {
		t.Fatalf("failed to write saved token: %v", err)
	}

	p := provider.NewCopilotProvider(config.CopilotConfig{
		OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN",
		DefaultModel:  "gpt-4.1",
	})

	// A cancelled context makes the reachability probe fail immediately
	// without dialling out. The assertion is about token detection, not
	// network success.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	line := copilotDoctorLine(p, ctx, "gpt-4.1")

	if strings.Contains(line, "no GitHub OAuth token") {
		t.Fatalf("doctor reported no token despite a saved device-flow token: %q", line)
	}
	if !strings.Contains(line, "copilot") {
		t.Errorf("doctor line should name the provider: %q", line)
	}
}

// With no token anywhere, doctor must still say so and point at the fix.
func TestDoctorReportsMissingCopilotToken(t *testing.T) {
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "")
	isolateConfigDir(t)

	p := provider.NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	line := copilotDoctorLine(p, ctx, "gpt-4.1")

	if !strings.Contains(line, "no GitHub OAuth token") {
		t.Errorf("doctor should report the missing token: %q", line)
	}
	if !strings.Contains(line, "gocode auth copilot") {
		t.Errorf("doctor should point at the login command: %q", line)
	}
	if !strings.Contains(line, "GOCODE_TEST_COPILOT_TOKEN") {
		t.Errorf("doctor should name the env var: %q", line)
	}
}
