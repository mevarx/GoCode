package mcp

import (
	"os"
	"strings"
	"testing"
)

func cmdEnvHasKey(env []string, name string) bool {
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i >= 0 && strings.EqualFold(entry[:i], name) {
			return true
		}
	}
	return false
}

func cmdEnvHasValue(env []string, name, value string) bool {
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i >= 0 &&
			strings.EqualFold(entry[:i], name) && entry[i+1:] == value {
			return true
		}
	}
	return false
}

func TestClient_EnvScrubExcludesSecrets(t *testing.T) {
	t.Setenv("MCP_TEST_SCRUB_ME", "mcp-super-secret")

	client := NewClientWithEnvScrub(
		os.Args[0],
		[]string{"-test.run=TestFakeMCPServerProcess"},
		map[string]string{
			"GO_FAKE_MCP_SERVER": "1",
			"FOO":                "bar",
		},
		[]string{"MCP_TEST_SCRUB_ME"},
	)
	if err := client.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	if cmdEnvHasKey(client.cmd.Env, "MCP_TEST_SCRUB_ME") {
		t.Errorf("expected scrubbed variable to be absent from MCP server env")
	}
	for _, entry := range client.cmd.Env {
		if strings.Contains(entry, "mcp-super-secret") {
			t.Errorf("expected secret value to be absent from MCP server env, found %q", entry)
		}
	}
	if !cmdEnvHasValue(client.cmd.Env, "FOO", "bar") {
		t.Errorf("expected server-configured FOO=bar to be present")
	}
	if !cmdEnvHasValue(client.cmd.Env, "GO_FAKE_MCP_SERVER", "1") {
		t.Errorf("expected server-configured GO_FAKE_MCP_SERVER=1 to be present")
	}
}
