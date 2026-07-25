package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config represents top-level configuration options.
type Config struct {
	Provider ProviderConfig `toml:"provider"`
	Approval ApprovalConfig `toml:"approval"`
	Session  SessionConfig  `toml:"session"`
}

// ProviderConfig specifies provider settings.
type ProviderConfig struct {
	Default    string           `toml:"default"`
	Ollama     OllamaConfig     `toml:"ollama"`
	OpenRouter OpenRouterConfig `toml:"openrouter"`
	Anthropic  AnthropicConfig  `toml:"anthropic"`
}

// OllamaConfig configures local Ollama instance.
type OllamaConfig struct {
	Host         string `toml:"host"`
	DefaultModel string `toml:"default_model"`
}

// OpenRouterConfig configures OpenRouter provider.
type OpenRouterConfig struct {
	APIKeyEnv    string `toml:"api_key_env"`
	DefaultModel string `toml:"default_model"`
}

// AnthropicConfig configures Anthropic provider.
type AnthropicConfig struct {
	APIKeyEnv    string `toml:"api_key_env"`
	DefaultModel string `toml:"default_model"`
}

// ApprovalConfig specifies tool approval thresholds.
type ApprovalConfig struct {
	AutoApproveReads  bool `toml:"auto_approve_reads"`
	AutoApproveWrites bool `toml:"auto_approve_writes"`
	AutoApproveShell  bool `toml:"auto_approve_shell"`
}

// SessionConfig specifies session settings.
type SessionConfig struct {
	Persist    bool   `toml:"persist"`
	HistoryDir string `toml:"history_dir"`
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		Provider: ProviderConfig{
			Default: "ollama",
			Ollama: OllamaConfig{
				Host:         "http://localhost:11434",
				DefaultModel: "",
			},
			OpenRouter: OpenRouterConfig{
				APIKeyEnv:    "OPENROUTER_API_KEY",
				DefaultModel: "anthropic/claude-sonnet-4.5",
			},
			Anthropic: AnthropicConfig{
				APIKeyEnv:    "ANTHROPIC_API_KEY",
				DefaultModel: "claude-sonnet-4-20250514",
			},
		},
		Approval: ApprovalConfig{
			AutoApproveReads:  true,
			AutoApproveWrites: false,
			AutoApproveShell:  false,
		},
		Session: SessionConfig{
			Persist:    true,
			HistoryDir: SessionDir(),
		},
	}
}

// Load reads configuration from file or returns defaults if non-existent.
func Load() (Config, error) {
	cfg := DefaultConfig()

	path := ConfigFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	if cfg.Session.HistoryDir == "" {
		cfg.Session.HistoryDir = SessionDir()
	}

	return cfg, nil
}
