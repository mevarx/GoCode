package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

func TestMCPTool_SpecAndExecution(t *testing.T) {
	info := ToolInfo{
		Name:        "get_weather",
		Description: "Get the current weather",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
	}

	mcpTool := NewMCPTool("weather_srv", info, nil)
	spec := mcpTool.Spec()

	if spec.Name != "weather_srv_get_weather" {
		t.Errorf("expected tool name weather_srv_get_weather, got %s", spec.Name)
	}
	if !strings.Contains(spec.Description, "[MCP: weather_srv]") {
		t.Errorf("expected description prefix, got %s", spec.Description)
	}
	if !mcpTool.RequiresApproval() {
		t.Error("MCP tools should require approval by default")
	}
}

func TestManager_AddListRemove(t *testing.T) {
	mgr := NewManager(nil)
	mgr.AddServer("local-fs", config.MCPServerConfig{
		Command: "echo",
		Args:    []string{"hello"},
	})

	servers := mgr.ListServers()
	if len(servers) != 1 || servers["local-fs"].Command != "echo" {
		t.Fatalf("expected 1 server, got %+v", servers)
	}

	mgr.RemoveServer("local-fs")
	if len(mgr.ListServers()) != 0 {
		t.Fatalf("expected 0 servers after removal")
	}
}
