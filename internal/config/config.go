package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
// Uses a long-lived GitHub OAuth token exchanged for a short-lived JWT; see internal/provider/copilot.go.
type CopilotConfig struct {
	// OAuthTokenEnv names the env var holding the GitHub OAuth token, never written into config.toml.
	OAuthTokenEnv string `toml:"oauth_token_env"`
	DefaultModel  string `toml:"default_model"`
	// EditorVersion and EditorPluginVersion are sent as Copilot headers; requests without them are rejected.
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
	// RequestTimeoutSeconds bounds a single JSON-RPC request to this server.
	// Zero or negative uses the built-in default of 60 seconds.
	RequestTimeoutSeconds int `toml:"request_timeout_seconds"`
}

// SessionConfig specifies session settings.
type SessionConfig struct {
	// Persist controls whether conversations are written to the SQLite store.
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
	// MaxContextTokens overrides the per-model context window; zero or
	// negative derives the budget from the active model's window (80%).
	MaxContextTokens int `toml:"max_context_tokens"`
	// VerifyCommand, when set, is run once after any provider round that
	// executed a tool able to modify the workspace. Empty disables auto-verify.
	VerifyCommand string `toml:"verify_command"`
	// VerifyTimeoutSeconds bounds one auto-verify run. Zero or negative uses
	// 120 seconds; it is only consulted when VerifyCommand is set.
	VerifyTimeoutSeconds int `toml:"verify_timeout_seconds"`
}

type ShellConfig struct {
	TimeoutSeconds int `toml:"timeout_seconds"`
	MaxOutputBytes int `toml:"max_output_bytes"`
	// RedactSecrets controls masking of shell_exec command output only, before
	// that output becomes a tool result. Setting it false does not disable
	// redaction at the transcript boundary: tool results, tool-call arguments,
	// and session exports are always redacted (see tools.RedactToolOutput).
	RedactSecrets bool `toml:"redact_secrets"`
	// EnvScrub lists additional env var names or patterns to drop from
	// child process environments, on top of the built-in credential patterns.
	EnvScrub []string `toml:"env_scrub"`
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
			// Hermes gateway on 127.0.0.1:8642, gated by API_SERVER_KEY.
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
			MaxContextTokens:     0,
			VerifyTimeoutSeconds: 120,
		},
		MCP: MCPConfig{
			Servers: make(map[string]MCPServerConfig),
		},
	}
}

// namedGateway pairs a gateway config with the provider name used in messages.
type namedGateway struct {
	name string
	cfg  GatewayConfig
}

// allGateways returns every configured gateway: the built-ins in a stable
// order, then custom providers sorted by name.
func allGateways(p ProviderConfig) []namedGateway {
	gateways := []namedGateway{
		{"omniroute", p.OmniRoute},
		{"openai", p.OpenAI},
		{"gemini", p.Gemini},
		{"groq", p.Groq},
		{"openrouter", p.OpenRouter},
		{"anthropic", p.Anthropic},
		{"qwen", p.Qwen},
		{"kimi", p.Kimi},
		{"hermes", p.Hermes},
		{"xai", p.XAI},
		{"mistral", p.Mistral},
		{"minimax", p.MiniMax},
		{"deepseek", p.DeepSeek},
		{"together", p.Together},
		{"fireworks", p.Fireworks},
		{"cerebras", p.Cerebras},
		{"zhipu", p.Zhipu},
		{"nvidia", p.Nvidia},
	}

	customNames := make([]string, 0, len(p.Custom))
	for name := range p.Custom {
		customNames = append(customNames, name)
	}
	sort.Strings(customNames)
	for _, name := range customNames {
		gateways = append(gateways, namedGateway{name, p.Custom[name]})
	}
	return gateways
}

// ValidateProviderBaseURL checks an OpenAI-compatible endpoint. Endpoints must
// be absolute http(s) URLs without credentials, a query, or a fragment, and
// plain HTTP is only allowed for localhost/loopback targets. A trailing slash
// is trimmed.
func ValidateProviderBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("base URL must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("base URL must not contain credentials, a query, or a fragment")
	}
	if parsed.Scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && !strings.HasSuffix(host, ".localhost") && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("base URL must use HTTPS unless it targets localhost")
		}
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// validateProviderBaseURLs rejects a config whose effective endpoint would send
// a key over cleartext or to a malformed URL. Copilot and Ollama have their own
// host handling and are not checked here.
func validateProviderBaseURLs(cfg Config) error {
	for _, gw := range allGateways(cfg.Provider) {
		if gw.cfg.BaseURL == "" {
			continue
		}
		if _, err := ValidateProviderBaseURL(gw.cfg.BaseURL); err != nil {
			return fmt.Errorf("provider %q has invalid base_url: %w", gw.name, err)
		}
	}
	return nil
}

// InlineAPIKeyWarnings reports every provider whose key is stored in config.toml
// instead of an environment variable. The key value itself is never included.
func InlineAPIKeyWarnings(cfg Config) []string {
	var warnings []string
	for _, gw := range allGateways(cfg.Provider) {
		if gw.cfg.APIKey != "" {
			warnings = append(warnings, fmt.Sprintf("⚠ %s: inline api_key stored in plaintext; prefer api_key_env", gw.name))
		}
	}
	return warnings
}

// SecretEnvNames returns every non-empty environment variable name that can
// hold a provider credential, plus the shell's extra scrub entries. Child
// processes started by GoCode get these variables removed.
func SecretEnvNames(cfg Config) []string {
	names := make([]string, 0, 24)
	seen := make(map[string]bool)

	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	p := cfg.Provider
	for _, gw := range allGateways(p) {
		add(gw.cfg.APIKeyEnv)
	}

	add(p.Copilot.OAuthTokenEnv)

	for _, name := range cfg.Tools.Shell.EnvScrub {
		add(name)
	}

	return names
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
	_ = os.Chmod(path, 0o600)

	if err := decodeConfig(data, &cfg, path); err != nil {
		return cfg, err
	}
	if err := validateProviderBaseURLs(cfg); err != nil {
		return cfg, fmt.Errorf("config file %s: %w", path, err)
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
	_ = os.Chmod(path, 0o600)

	if err := decodeConfig(data, &cfg, path); err != nil {
		return cfg, err
	}
	if err := validateProviderBaseURLs(cfg); err != nil {
		return cfg, fmt.Errorf("config file %s: %w", path, err)
	}

	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]MCPServerConfig)
	}
	if cfg.Provider.Custom == nil {
		cfg.Provider.Custom = make(map[string]GatewayConfig)
	}

	return cfg, nil
}

// decodeConfig parses TOML and rejects unknown keys; a misspelled key would
// otherwise silently keep the default and disable a control the user set.
func decodeConfig(data []byte, cfg *Config, path string) error {
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return fmt.Errorf("config file %s contains unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return nil
}

// Save writes the configuration to the config.toml file.
func Save(cfg Config) error {
	return writeConfigFile(cfg, ConfigFilePath())
}

// SaveToPath writes the configuration to a specific file path.
func SaveToPath(cfg Config, path string) error {
	return writeConfigFile(cfg, path)
}

// writeConfigFile creates the parent directory owner-only and writes path with
// mode 0600. An existing file keeps its old mode through O_CREATE, so chmod it
// as well.
func writeConfigFile(cfg Config, path string) error {
	// A bare filename has "." as its parent; chmod'ing the working directory
	// would be surprising, so only treat a real directory as one to create.
	if dir := filepath.Dir(path); dir != "." {
		if err := ensurePrivateDir(dir); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer f.Close()

	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to restrict config file permissions: %w", err)
	}

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("failed to encode config to TOML: %w", err)
	}

	return nil
}
