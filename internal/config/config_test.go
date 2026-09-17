package config

import (
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
}
