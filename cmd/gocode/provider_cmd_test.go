package main

import (
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/provider"
)

func TestAddCustomProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	name, endpoint, err := addCustomProvider(&cfg, " My-Endpoint ", config.GatewayConfig{
		BaseURL:      "https://example.com/v1/",
		APIKeyEnv:    "EXAMPLE_API_KEY",
		DefaultModel: "example-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-endpoint" {
		t.Fatalf("expected normalized provider name, got %q", name)
	}
	if endpoint.BaseURL != "https://example.com/v1" {
		t.Fatalf("expected trailing slash to be trimmed, got %q", endpoint.BaseURL)
	}
	if cfg.Provider.Custom[name] != endpoint {
		t.Fatalf("expected provider endpoint to be saved in config, got %+v", cfg.Provider.Custom)
	}
}

func TestAddCustomProviderRejectsUnsafeOrInvalidValues(t *testing.T) {
	tests := []struct {
		testName     string
		providerName string
		endpoint     config.GatewayConfig
		contains     string
	}{
		{"built-in name", "openai", config.GatewayConfig{BaseURL: "https://example.com", DefaultModel: "m"}, "built-in"},
		{"invalid URL", "example", config.GatewayConfig{BaseURL: "file:///tmp/api", DefaultModel: "m"}, "absolute http or https"},
		{"insecure remote URL", "example", config.GatewayConfig{BaseURL: "http://api.example.com/v1", APIKeyEnv: "EXAMPLE_API_KEY", DefaultModel: "m"}, "must use HTTPS"},
		{"URL credentials", "example", config.GatewayConfig{BaseURL: "https://user:pass@example.com", DefaultModel: "m"}, "must not contain credentials"},
		{"URL query", "example", config.GatewayConfig{BaseURL: "https://example.com?key=secret", DefaultModel: "m"}, "must not contain credentials"},
		{"invalid key env", "example", config.GatewayConfig{BaseURL: "https://example.com", APIKeyEnv: "not valid", DefaultModel: "m"}, "environment variable"},
		{"missing model", "example", config.GatewayConfig{BaseURL: "https://example.com"}, "model cannot be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			_, _, err := addCustomProvider(&config.Config{}, tt.providerName, tt.endpoint)
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("expected error containing %q, got %v", tt.contains, err)
			}
		})
	}
}

func TestRegisterCustomProviders(t *testing.T) {
	registry := provider.NewRegistry()
	configured := map[string]config.GatewayConfig{
		"deepseek": {
			BaseURL:      "https://api.deepseek.com",
			APIKeyEnv:    "DEEPSEEK_API_KEY",
			DefaultModel: "deepseek-flash",
		},
	}
	if err := registerCustomProviders(registry, configured); err != nil {
		t.Fatal(err)
	}
	if got := registry.Get("deepseek"); got == nil {
		t.Fatal("expected custom provider to be registered")
	}
	if got := gatewayConfigFor(config.ProviderConfig{Custom: configured}, "deepseek"); got.DefaultModel != "deepseek-flash" {
		t.Fatalf("expected custom provider default model, got %+v", got)
	}
}
