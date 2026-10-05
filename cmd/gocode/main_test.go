package main

import (
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

func TestGatewayConfigFor(t *testing.T) {
	cfg := config.DefaultConfig().Provider

	gw := gatewayConfigFor(cfg, "openai")
	if gw.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected openai base URL, got %s", gw.BaseURL)
	}

	gwUnknown := gatewayConfigFor(cfg, "nonexistent")
	if gwUnknown.BaseURL != "" {
		t.Errorf("expected empty gateway for unknown provider, got %+v", gwUnknown)
	}
}

// A mismatch here would silently register a provider with an empty base URL.
func TestGatewayConfigForCoversEveryRegisteredGateway(t *testing.T) {
	cfg := config.DefaultConfig().Provider
	for _, name := range gatewayProviderNames {
		if gatewayConfigFor(cfg, name).BaseURL == "" {
			t.Errorf("gateway %q resolves to an empty base URL", name)
		}
	}
}

// gatewayConfigFor also serves the special providers.
func TestGatewayConfigForSpecialProviders(t *testing.T) {
	cfg := config.DefaultConfig().Provider
	if got := gatewayConfigFor(cfg, "anthropic").BaseURL; got != "https://api.anthropic.com/v1" {
		t.Errorf("anthropic base URL wrong: %q", got)
	}
	if got := gatewayConfigFor(cfg, "not-a-provider").BaseURL; got != "" {
		t.Errorf("unknown name should not resolve, got %q", got)
	}
}

func TestGatewayConfigForReadsCustomMap(t *testing.T) {
	cfg := config.DefaultConfig().Provider
	cfg.Custom["deepseek"] = config.GatewayConfig{BaseURL: "https://api.deepseek.com"}

	if got := gatewayConfigFor(cfg, "deepseek").BaseURL; got != "https://api.deepseek.com" {
		t.Errorf("expected custom provider base URL, got %q", got)
	}
}

// Every selectable provider must be registered, or --provider <name> fails at startup.
func TestGatewayProviderNamesAreAllDistinct(t *testing.T) {
	seen := make(map[string]bool, len(gatewayProviderNames))
	for _, name := range gatewayProviderNames {
		if name == "" {
			t.Error("empty provider name in gatewayProviderNames")
		}
		if seen[name] {
			t.Errorf("duplicate provider name %q in gatewayProviderNames", name)
		}
		seen[name] = true
	}
	if seen["ollama"] {
		t.Error("ollama is registered separately and must not be in gatewayProviderNames")
	}
	if seen["anthropic"] {
		t.Error("anthropic has a native provider and must not be in gatewayProviderNames")
	}
}
