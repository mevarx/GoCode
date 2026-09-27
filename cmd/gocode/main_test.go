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
