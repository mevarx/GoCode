package config

import (
	"os"
	"path/filepath"
	"runtime"
)

const appName = "gocode"

func ConfigDir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(base, appName)
	}

	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, appName)
}

func DataDir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
		return filepath.Join(base, appName)
	}

	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, appName)
}

func SessionDir() string {
	return filepath.Join(DataDir(), "sessions")
}

func LogDir() string {
	return filepath.Join(DataDir(), "logs")
}

func LogFilePath() string {
	return filepath.Join(LogDir(), "gocode.log")
}

func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), "config.toml")
}

// CopilotTokenPath stores the GitHub OAuth token in the config dir (a credential, written owner-only).
func CopilotTokenPath() string {
	return filepath.Join(ConfigDir(), "copilot_token")
}

// EnsureDirs creates the app's config and data directories, owner-only.
func EnsureDirs() error {
	for _, dir := range []string{ConfigDir(), DataDir(), SessionDir(), LogDir()} {
		if err := ensurePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// ensurePrivateDir creates dir if needed and tightens an existing one: MkdirAll
// leaves the mode of an existing directory untouched, so a directory created
// earlier with a looser mode would otherwise stay readable by other users.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
