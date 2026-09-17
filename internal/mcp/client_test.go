package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

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

func TestClient_ReadLoop_ContentLengthAndNewline(t *testing.T) {
	pr, pw := io.Pipe()
	client := NewClient("dummy", nil, nil)
	client.stdout = pr

	respCh := make(chan *JSONRPCResponse, 1)
	client.pending[1] = respCh

	go client.readLoop()

	// 1. Send Content-Length framed message
	body := `{"jsonrpc":"2.0","id":1,"result":"ok"}`
	_, _ = pw.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)))

	select {
	case resp := <-respCh:
		if resp == nil || string(resp.Result) != `"ok"` {
			t.Fatalf("expected ok result, got %+v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Content-Length message")
	}

	// 2. Send newline-delimited message
	respCh2 := make(chan *JSONRPCResponse, 1)
	client.mu.Lock()
	client.pending[2] = respCh2
	client.mu.Unlock()

	_, _ = pw.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":\"newline-ok\"}\n"))

	select {
	case resp2 := <-respCh2:
		if resp2 == nil || string(resp2.Result) != `"newline-ok"` {
			t.Fatalf("expected newline-ok result, got %+v", resp2)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for newline-delimited message")
	}

	pw.Close()
}
