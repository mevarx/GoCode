package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config represents top-level configuration options.
type Config struct {
	Provider    ProviderConfig    `toml:"provider"`
	Permissions PermissionsConfig `toml:"permissions"`
	Session     SessionConfig     `toml:"session"`
	Tools       ToolsConfig       `toml:"tools"`
	MCP         MCPConfig         `toml:"mcp"`
}

// ProviderConfig specifies provider settings.
type ProviderConfig struct {
	Default    string                   `toml:"default"`
	Ollama     OllamaConfig             `toml:"ollama"`
	OmniRoute  GatewayConfig            `toml:"omniroute"`
	OpenAI     GatewayConfig            `toml:"openai"`
	Gemini     GatewayConfig            `toml:"gemini"`
	Groq       GatewayConfig            `toml:"groq"`
	OpenRouter GatewayConfig            `toml:"openrouter"`
	Anthropic  GatewayConfig            `toml:"anthropic"`
	Qwen       GatewayConfig            `toml:"qwen"`
	Kimi       GatewayConfig            `toml:"kimi"`
	Hermes     GatewayConfig            `toml:"hermes"`
	XAI        GatewayConfig            `toml:"xai"`
	Mistral    GatewayConfig            `toml:"mistral"`
	MiniMax    GatewayConfig            `toml:"minimax"`
	DeepSeek   GatewayConfig            `toml:"deepseek"`
	Together   GatewayConfig            `toml:"together"`
	Fireworks  GatewayConfig            `toml:"fireworks"`
	Cerebras   GatewayConfig            `toml:"cerebras"`
	Zhipu      GatewayConfig            `toml:"zhipu"`
	Nvidia     GatewayConfig            `toml:"nvidia"`
	Copilot    CopilotConfig            `toml:"copilot"`
	Custom     map[string]GatewayConfig `toml:"custom"`
}

// CopilotConfig configures GitHub Copilot.
//
// Copilot is not a plain bearer-token API. It needs a long-lived GitHub OAuth
// token, which is exchanged for a short-lived Copilot JWT that also carries a
// dynamic API base URL. Both layers are modelled here; see
// internal/provider/copilot.go for the exchange.
type CopilotConfig struct {
	// OAuthTokenEnv names the environment variable holding a GitHub OAuth
	// token (the `ghu_...` form produced by `gocode auth copilot`). The token
	// itself is never written into config.toml.
	OAuthTokenEnv string `toml:"oauth_token_env"`
	DefaultModel  string `toml:"default_model"`
	// EditorVersion and EditorPluginVersion are sent as the Copilot
	// integration headers. Copilot rejects requests whose headers do not look
	// like a real editor client.
	EditorVersion       string `toml:"editor_version"`
	EditorPluginVersion string `toml:"editor_plugin_version"`
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

// PermissionsConfig specifies granular tool permissions.
type PermissionsConfig struct {
	AutoApprove       []string `toml:"auto_approve"`
	Deny              []string `toml:"deny"`
	SensitivePatterns []string `toml:"sensitive_patterns"`
}

// MCPConfig specifies Model Context Protocol server configurations.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `toml:"servers"`
}

// MCPServerConfig defines an individual MCP server command and arguments.
type MCPServerConfig struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env,omitempty"`
}

// SessionConfig specifies session settings.
type SessionConfig struct {
	// Persist controls whether conversations are written to the SQLite
	// store. It was previously parsed and then ignored, so `persist = false`
	// had no effect while the documentation implied it did.
	Persist bool `toml:"persist"`
}

type ToolsConfig struct {
	Shell ShellConfig `toml:"shell"`
	// MaxToolIterations caps provider round-trips per user turn. Zero or
	// negative uses the built-in default.
	MaxToolIterations int `toml:"max_tool_iterations"`
	// MaxRepeatedToolCalls caps identical tool calls (same name and same
	// arguments) within one turn. Zero or negative uses the built-in default.
	MaxRepeatedToolCalls int `toml:"max_repeated_tool_calls"`
}

type ShellConfig struct {
	TimeoutSeconds int  `toml:"timeout_seconds"`
	MaxOutputBytes int  `toml:"max_output_bytes"`
	RedactSecrets  bool `toml:"redact_secrets"`
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
			// Hermes exposes itself as an OpenAI-compatible endpoint on
			// 127.0.0.1:8642, gated by API_SERVER_KEY. This makes a running
			// Hermes gateway usable as a GoCode backend (and vice versa).
			Hermes: GatewayConfig{
				BaseURL:      "http://127.0.0.1:8642/v1",
				APIKeyEnv:    "HERMES_API_SERVER_KEY",
				DefaultModel: "hermes-agent",
			},
			XAI: GatewayConfig{
				BaseURL:      "https://api.x.ai/v1",
				APIKeyEnv:    "XAI_API_KEY",
				DefaultModel: "grok-code-fast-1",
			},
			Mistral: GatewayConfig{
				BaseURL:      "https://api.mistral.ai/v1",
				APIKeyEnv:    "MISTRAL_API_KEY",
				DefaultModel: "mistral-large-latest",
			},
			MiniMax: GatewayConfig{
				BaseURL:      "https://api.minimax.io/v1",
				APIKeyEnv:    "MINIMAX_API_KEY",
				DefaultModel: "MiniMax-M2.5",
			},
			DeepSeek: GatewayConfig{
				BaseURL:      "https://api.deepseek.com/v1",
				APIKeyEnv:    "DEEPSEEK_API_KEY",
				DefaultModel: "deepseek-chat",
			},
			Together: GatewayConfig{
				BaseURL:      "https://api.together.xyz/v1",
				APIKeyEnv:    "TOGETHER_API_KEY",
				DefaultModel: "Qwen/Qwen3-Coder-480B-A35B-Instruct",
			},
			Fireworks: GatewayConfig{
				BaseURL:      "https://api.fireworks.ai/inference/v1",
				APIKeyEnv:    "FIREWORKS_API_KEY",
				DefaultModel: "accounts/fireworks/models/kimi-k2-thinking",
			},
			Cerebras: GatewayConfig{
				BaseURL:      "https://api.cerebras.ai/v1",
				APIKeyEnv:    "CEREBRAS_API_KEY",
				DefaultModel: "qwen-3-coder-480b",
			},
			Zhipu: GatewayConfig{
				BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
				APIKeyEnv:    "ZHIPU_API_KEY",
				DefaultModel: "glm-4.6",
			},
			Nvidia: GatewayConfig{
				BaseURL:      "https://integrate.api.nvidia.com/v1",
				APIKeyEnv:    "NVIDIA_API_KEY",
				DefaultModel: "qwen/qwen3-coder-480b-a35b-instruct",
			},
			Copilot: CopilotConfig{
				OAuthTokenEnv:       "GITHUB_COPILOT_TOKEN",
				DefaultModel:        "gpt-4.1",
				EditorVersion:       "vscode/1.111.0",
				EditorPluginVersion: "copilot-chat/0.40.0",
			},
			Custom: make(map[string]GatewayConfig),
		},
		Permissions: PermissionsConfig{
			AutoApprove:       []string{"file_read"},
			Deny:              []string{},
			SensitivePatterns: []string{},
		},
		Session: SessionConfig{
			Persist: true,
		},
		Tools: ToolsConfig{
			Shell: ShellConfig{
				TimeoutSeconds: 30,
				MaxOutputBytes: 1024 * 1024,
				RedactSecrets:  true,
			},
		},
		MCP: MCPConfig{
			Servers: make(map[string]MCPServerConfig),
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

	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]MCPServerConfig)
	}
	if cfg.Provider.Custom == nil {
		cfg.Provider.Custom = make(map[string]GatewayConfig)
	}

	return cfg, nil
}

// LoadFromPath reads configuration from a specific file path.
func LoadFromPath(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]MCPServerConfig)
	}
	if cfg.Provider.Custom == nil {
		cfg.Provider.Custom = make(map[string]GatewayConfig)
	}

	return cfg, nil
}

// Save writes the configuration to the config.toml file.
func Save(cfg Config) error {
	path := ConfigFilePath()
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer f.Close()

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("failed to encode config to TOML: %w", err)
	}

	return nil
}

// SaveToPath writes the configuration to a specific file path.
func SaveToPath(cfg Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer f.Close()

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("failed to encode config to TOML: %w", err)
	}

	return nil
}
