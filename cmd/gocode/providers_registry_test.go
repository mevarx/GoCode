package main

import (
	"bytes"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/provider"
)

// Every gateway name must resolve to a real endpoint, or failures surface only at request time.
func TestEveryGatewayProviderResolvesToAnEndpoint(t *testing.T) {
	cfg := config.DefaultConfig().Provider
	for _, name := range gatewayProviderNames {
		endpoint := gatewayConfigFor(cfg, name)
		if endpoint.BaseURL == "" {
			t.Errorf("provider %q resolves to an empty base URL: gatewayProviderNames and gatewayConfigFor are out of sync", name)
		}
		if endpoint.DefaultModel == "" {
			t.Errorf("provider %q has no default model", name)
		}
		// Local gateways authenticate nothing, so an empty key env is valid
		// for them; every hosted provider must name one.
		if endpoint.APIKeyEnv == "" && !isLocalGateway(endpoint.BaseURL) {
			t.Errorf("provider %q is hosted but does not name an API key environment variable", name)
		}
	}
}

// isLocalGateway reports loopback URLs, which typically need no credential.
func isLocalGateway(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// The providers added for v0.5.2 must all be present in the registry.
func TestNewProvidersAreRegistered(t *testing.T) {
	cfg := config.DefaultConfig().Provider
	expected := map[string]string{
		"hermes":    "http://127.0.0.1:8642/v1",
		"xai":       "https://api.x.ai/v1",
		"mistral":   "https://api.mistral.ai/v1",
		"minimax":   "https://api.minimax.io/v1",
		"deepseek":  "https://api.deepseek.com/v1",
		"together":  "https://api.together.xyz/v1",
		"fireworks": "https://api.fireworks.ai/inference/v1",
		"cerebras":  "https://api.cerebras.ai/v1",
		"zhipu":     "https://open.bigmodel.cn/api/paas/v4",
		"nvidia":    "https://integrate.api.nvidia.com/v1",
	}
	for name, wantBase := range expected {
		if !isBuiltInProvider(name) {
			t.Errorf("provider %q should be built-in", name)
			continue
		}
		got := gatewayConfigFor(cfg, name)
		if got.BaseURL != wantBase {
			t.Errorf("provider %q base URL = %q, want %q", name, got.BaseURL, wantBase)
		}
	}
}

// A custom name that later became built-in must still resolve to the user's endpoint.
func TestCustomProviderOverridesBuiltInOfSameName(t *testing.T) {
	cfg := config.DefaultConfig().Provider
	cfg.Custom["deepseek"] = config.GatewayConfig{
		BaseURL:      "https://my-proxy.internal/v1",
		APIKeyEnv:    "MY_DEEPSEEK_KEY",
		DefaultModel: "deepseek-reasoner",
	}

	got := gatewayConfigFor(cfg, "deepseek")
	if got.BaseURL != "https://my-proxy.internal/v1" {
		t.Errorf("custom override lost: got base URL %q", got.BaseURL)
	}
	if got.DefaultModel != "deepseek-reasoner" {
		t.Errorf("custom override lost: got model %q", got.DefaultModel)
	}
}

// A saved custom name that is now built-in must not break startup.
func TestRegisterCustomProvidersAcceptsNewlyBuiltInName(t *testing.T) {
	registry := provider.NewRegistry()
	configured := map[string]config.GatewayConfig{
		"deepseek": {
			BaseURL:      "https://api.deepseek.com",
			APIKeyEnv:    "DEEPSEEK_API_KEY",
			DefaultModel: "deepseek-flash",
		},
	}
	if err := registerCustomProviders(registry, configured); err != nil {
		t.Fatalf("a saved custom provider must not break startup: %v", err)
	}
	if got := registry.Get("deepseek"); got == nil {
		t.Fatal("expected custom deepseek to be registered")
	}
}

// copilot is wired as its own provider type, not a gateway proxy.
func TestCopilotIsBuiltInAndConfigured(t *testing.T) {
	if !isBuiltInProvider("copilot") {
		t.Error("copilot should be a built-in provider name")
	}
	copilotCfg := config.DefaultConfig().Provider.Copilot
	if copilotCfg.DefaultModel == "" {
		t.Error("copilot needs a default model")
	}
	if copilotCfg.OAuthTokenEnv == "" {
		t.Error("copilot needs an OAuth token environment variable")
	}
	if copilotCfg.EditorVersion == "" || copilotCfg.EditorPluginVersion == "" {
		t.Error("copilot needs editor version headers or GitHub rejects requests")
	}
}

// `provider list` keeps its own name list, so every registered provider must appear in the output.
func TestProviderListIncludesEveryRegisteredProvider(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.SaveToPath(config.DefaultConfig(), cfgPath); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cmd := newProviderCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// --config is a persistent flag on the root command, so exercise the same
	// loader the real CLI uses by setting the shared variable.
	prevConfig := flagConfig
	flagConfig = cfgPath
	defer func() { flagConfig = prevConfig }()

	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("provider list failed: %v", err)
	}

	out := cmd.OutOrStdout().(*bytes.Buffer).String()

	registered := append([]string(nil), gatewayProviderNames...)
	registered = append(registered, "anthropic", "copilot", "ollama")
	for _, name := range registered {
		if !strings.Contains(out, name) {
			t.Errorf("provider %q is registered but missing from `provider list`", name)
		}
	}

	// The default provider must be discoverable without reading config.toml.
	if cfg := config.DefaultConfig(); !strings.Contains(out, cfg.Provider.Default) {
		t.Errorf("the default provider %q is not listed", cfg.Provider.Default)
	}
}
