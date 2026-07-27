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
	Default    string        `toml:"default"`
	Ollama     OllamaConfig  `toml:"ollama"`
	OmniRoute  GatewayConfig `toml:"omniroute"`
	OpenAI     GatewayConfig `toml:"openai"`
	Gemini     GatewayConfig `toml:"gemini"`
	Groq       GatewayConfig `toml:"groq"`
	OpenRouter GatewayConfig `toml:"openrouter"`
	Anthropic  GatewayConfig `toml:"anthropic"`
	Qwen       GatewayConfig `toml:"qwen"`
	Kimi       GatewayConfig `toml:"kimi"`
}

// GatewayConfig configures an OpenAI-compatible gateway or cloud provider endpoint.
type GatewayConfig struct {
	BaseURL      string `toml:"base_url"`
	APIKey       string `toml:"api_key"`
	APIKeyEnv    string `toml:"api_key_env"`
	DefaultModel string `toml:"default_model"`
}

// OllamaConfig configures local Ollama instance.
type OllamaConfig struct {
	Host         string `toml:"host"`
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
				Host:         "http://127.0.0.1:11434",
				DefaultModel: "",
			},
			OmniRoute: GatewayConfig{
				BaseURL:      "http://127.0.0.1:20128/v1",
				APIKeyEnv:    "OMNIROUTE_API_KEY",
				DefaultModel: "auto",
			},
			OpenAI: GatewayConfig{
				BaseURL:      "https://api.openai.com/v1",
				APIKeyEnv:    "OPENAI_API_KEY",
				DefaultModel: "gpt-4o",
			},
			Gemini: GatewayConfig{
				BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai",
				APIKeyEnv:    "GEMINI_API_KEY",
				DefaultModel: "gemini-2.5-flash",
			},
			Groq: GatewayConfig{
				BaseURL:      "https://api.groq.com/openai/v1",
				APIKeyEnv:    "GROQ_API_KEY",
				DefaultModel: "llama-3.3-70b-versatile",
			},
			OpenRouter: GatewayConfig{
				BaseURL:      "https://openrouter.ai/api/v1",
				APIKeyEnv:    "OPENROUTER_API_KEY",
				DefaultModel: "anthropic/claude-sonnet-4.5",
			},
			Anthropic: GatewayConfig{
				BaseURL:      "https://api.anthropic.com/v1",
				APIKeyEnv:    "ANTHROPIC_API_KEY",
				DefaultModel: "claude-sonnet-4-20250514",
			},
			Qwen: GatewayConfig{
				BaseURL:      "https://dashscope.aliyuncs.com/compatible-mode/v1",
				APIKeyEnv:    "DASHSCOPE_API_KEY",
				DefaultModel: "qwen-max",
			},
			Kimi: GatewayConfig{
				BaseURL:      "https://api.moonshot.cn/v1",
				APIKeyEnv:    "MOONSHOT_API_KEY",
				DefaultModel: "moonshot-v1-8k",
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
