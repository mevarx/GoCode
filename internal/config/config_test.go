package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Provider.Default != "ollama" {
		t.Errorf("expected default provider ollama, got %s", cfg.Provider.Default)
	}

	if len(cfg.Permissions.AutoApprove) != 1 || cfg.Permissions.AutoApprove[0] != "file_read" {
		t.Errorf("expected default auto_approve ['file_read'], got %+v", cfg.Permissions.AutoApprove)
	}

	if len(cfg.Permissions.Deny) != 0 {
		t.Errorf("expected default deny [], got %+v", cfg.Permissions.Deny)
	}
}

func TestConfig_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := tmpDir + "/config.toml"

	cfg := DefaultConfig()
	cfg.Provider.Default = "openai"
	cfg.Permissions.AutoApprove = []string{"file_read", "file_write"}
	cfg.Permissions.Deny = []string{"shell_exec"}
	cfg.Permissions.SensitivePatterns = []string{"*.vault", "custom.env"}
	cfg.MCP.Servers["test-mcp"] = MCPServerConfig{
		Command: "node",
		Args:    []string{"server.js"},
	}
	cfg.Provider.Custom["deepseek"] = GatewayConfig{
		BaseURL:      "https://api.deepseek.com",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		DefaultModel: "deepseek-flash",
	}

	if err := SaveToPath(cfg, tmpFile); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if loaded.Provider.Default != "openai" {
		t.Errorf("expected provider default 'openai', got %q", loaded.Provider.Default)
	}
	if len(loaded.Permissions.AutoApprove) != 2 || loaded.Permissions.AutoApprove[0] != "file_read" {
		t.Errorf("unexpected auto_approve: %+v", loaded.Permissions.AutoApprove)
	}
	if len(loaded.Permissions.Deny) != 1 || loaded.Permissions.Deny[0] != "shell_exec" {
		t.Errorf("unexpected deny: %+v", loaded.Permissions.Deny)
	}
	if len(loaded.Permissions.SensitivePatterns) != 2 || loaded.Permissions.SensitivePatterns[0] != "*.vault" {
		t.Errorf("unexpected sensitive_patterns: %+v", loaded.Permissions.SensitivePatterns)
	}
	if srv, ok := loaded.MCP.Servers["test-mcp"]; !ok || srv.Command != "node" {
		t.Errorf("expected test-mcp server, got %+v", loaded.MCP.Servers)
	}
	if got := loaded.Provider.Custom["deepseek"]; got.BaseURL != "https://api.deepseek.com" || got.APIKeyEnv != "DEEPSEEK_API_KEY" || got.DefaultModel != "deepseek-flash" {
		t.Errorf("unexpected custom provider config: %+v", got)
	}
}

// The loop-guard keys were added in v0.5.0 and consumed by both UI paths. They
// must survive a save/load round-trip or a user's limits are silently ignored.
func TestConfig_LoopGuardKeysRoundTrip(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")

	cfg := DefaultConfig()
	cfg.Tools.MaxToolIterations = 12
	cfg.Tools.MaxRepeatedToolCalls = 2
	if err := SaveToPath(cfg, tmpFile); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Tools.MaxToolIterations != 12 {
		t.Errorf("expected max_tool_iterations 12, got %d", loaded.Tools.MaxToolIterations)
	}
	if loaded.Tools.MaxRepeatedToolCalls != 2 {
		t.Errorf("expected max_repeated_tool_calls 2, got %d", loaded.Tools.MaxRepeatedToolCalls)
	}
}

// Parsing the keys directly from TOML text proves the toml tags are right,
// independent of what Save happens to emit.
func TestConfig_LoopGuardKeysParseFromTOML(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	contents := "[tools]\nmax_tool_iterations = 7\nmax_repeated_tool_calls = 1\n"
	if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Tools.MaxToolIterations != 7 {
		t.Errorf("expected 7, got %d", loaded.Tools.MaxToolIterations)
	}
	if loaded.Tools.MaxRepeatedToolCalls != 1 {
		t.Errorf("expected 1, got %d", loaded.Tools.MaxRepeatedToolCalls)
	}
}

// session.persist was parsed and then ignored; it must round-trip so that
// `persist = false` actually reaches the wiring in main.go.
func TestConfig_SessionPersistRoundTrip(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte("[session]\npersist = false\n"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Session.Persist {
		t.Error("expected persist=false to load as false")
	}

	// And the default must stay true when the key is absent.
	if !DefaultConfig().Session.Persist {
		t.Error("expected default persist to be true")
	}
}

func TestConfig_ShellKeysRoundTrip(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	contents := "[tools.shell]\ntimeout_seconds = 90\nmax_output_bytes = 2048\nredact_secrets = false\n"
	if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Tools.Shell.TimeoutSeconds != 90 {
		t.Errorf("expected timeout 90, got %d", loaded.Tools.Shell.TimeoutSeconds)
	}
	if loaded.Tools.Shell.MaxOutputBytes != 2048 {
		t.Errorf("expected max_output_bytes 2048, got %d", loaded.Tools.Shell.MaxOutputBytes)
	}
	if loaded.Tools.Shell.RedactSecrets {
		t.Error("expected redact_secrets=false to load as false")
	}
}

// Every built-in provider must be present and reachable by name, since
// main.go registers each one from DefaultConfig.
func TestDefaultConfig_BuiltinProviders(t *testing.T) {
	cfg := DefaultConfig()
	checks := map[string]GatewayConfig{
		"openai":     cfg.Provider.OpenAI,
		"gemini":     cfg.Provider.Gemini,
		"groq":       cfg.Provider.Groq,
		"openrouter": cfg.Provider.OpenRouter,
		"anthropic":  cfg.Provider.Anthropic,
		"qwen":       cfg.Provider.Qwen,
		"kimi":       cfg.Provider.Kimi,
		"omniroute":  cfg.Provider.OmniRoute,
	}
	for name, gw := range checks {
		if gw.BaseURL == "" {
			t.Errorf("provider %q has no base URL", name)
		}
		if gw.APIKeyEnv == "" && name != "omniroute" {
			t.Errorf("provider %q has no APIKeyEnv", name)
		}
	}
	if cfg.Provider.Ollama.Host == "" {
		t.Error("ollama host is empty")
	}
	if cfg.Provider.Custom == nil {
		t.Error("Custom map must be initialized")
	}
}

// An empty config file must load cleanly with defaults, not error.
func TestLoadFromPath_EmptyFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, nil, 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("empty config should load, got: %v", err)
	}
	if loaded.Provider.Default != "ollama" {
		t.Errorf("expected defaults to be preserved, got provider %q", loaded.Provider.Default)
	}
}

func TestLoadFromPath_MissingFileErrors(t *testing.T) {
	if _, err := LoadFromPath(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("expected an error for a missing config file")
	}
}

func TestLoadFromPath_MalformedTOMLErrors(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := LoadFromPath(tmpFile); err == nil {
		t.Error("expected a parse error for malformed TOML")
	}
}

// MEDIUM-32: misspelled keys would silently keep defaults, so they must error.
func TestLoadFromPath_UnknownKeysError(t *testing.T) {
	for _, contents := range []string{
		"[permissions]\nauto_approv = [\"file_read\"]\n",
		"[tools]\nmax_tool_iteration = 5\n",
		"[session]\npresist = true\n",
		"[provider]\ndeafult = \"ollama\"\n",
	} {
		tmpFile := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if _, err := LoadFromPath(tmpFile); err == nil {
			t.Errorf("expected an unknown-key error for %q", contents)
		} else if !strings.Contains(err.Error(), "unknown keys") {
			t.Errorf("error should mention unknown keys, got: %v", err)
		}
	}
}

// Dynamic map entries (custom providers, MCP servers) are real keys and must keep loading.
func TestLoadFromPath_CustomAndMCPKeysAreKnown(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	contents := "[provider.custom.my-gateway]\nbase_url = \"https://gw.internal/v1\"\n" +
		"[mcp.servers.fs]\ncommand = \"npx\"\nargs = [\"-y\", \"srv\"]\n"
	if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("custom/mcp keys must load, got: %v", err)
	}
	if loaded.Provider.Custom["my-gateway"].BaseURL != "https://gw.internal/v1" {
		t.Errorf("custom provider lost: %+v", loaded.Provider.Custom)
	}
	if loaded.MCP.Servers["fs"].Command != "npx" {
		t.Errorf("mcp server lost: %+v", loaded.MCP.Servers)
	}
}

// MEDIUM-9/MEDIUM-34: context budget needs a sane default to avoid truncating long sessions.
func TestConfig_MaxContextTokensDefaultAndRoundTrip(t *testing.T) {
	if got := DefaultConfig().Tools.MaxContextTokens; got <= 0 {
		t.Fatalf("default MaxContextTokens must be positive, got %d", got)
	}

	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	contents := "[tools]\nmax_context_tokens = 12345\n"
	if err := os.WriteFile(tmpFile, []byte(contents), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	loaded, err := LoadFromPath(tmpFile)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Tools.MaxContextTokens != 12345 {
		t.Errorf("expected max_context_tokens 12345, got %d", loaded.Tools.MaxContextTokens)
	}
}
