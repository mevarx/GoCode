package config

import (
	"os"
	"path/filepath"
	"runtime"
)

const appName = "gocode"

// ConfigDir returns the configuration directory for GoCode.
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

// DataDir returns the data storage directory for GoCode.
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

// SessionDir returns the directory for session history files.
func SessionDir() string {
	return filepath.Join(DataDir(), "sessions")
}

// ConfigFilePath returns the path to config.toml.
func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), "config.toml")
}

// EnsureDirs creates configuration and data directories if missing.
func EnsureDirs() error {
	for _, dir := range []string{ConfigDir(), DataDir(), SessionDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
