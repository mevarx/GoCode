package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

	if spec.Name != "mcp_weather_srv_get_weather" {
		t.Errorf("expected tool name mcp_weather_srv_get_weather, got %s", spec.Name)
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

func TestFakeMCPServerProcess(t *testing.T) {
	if os.Getenv("GO_FAKE_MCP_SERVER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req JSONRPCRequest
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"serverInfo\":{\"name\":\"fake\",\"version\":\"0.0.1\"}}}\n", *req.ID)
		case "tools/list":
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"tools\":[{\"name\":\"echo\",\"description\":\"echo back\",\"inputSchema\":{\"type\":\"object\"}}]}}\n", *req.ID)
		case "tools/call":
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}],\"isError\":false}}\n", *req.ID)
		}
	}
	os.Exit(0)
}

func TestClient_HandshakeWithFakeServer(t *testing.T) {
	env := map[string]string{"GO_FAKE_MCP_SERVER": "1"}
	client := NewClient(os.Args[0], []string{"-test.run=TestFakeMCPServerProcess"}, env)
	if err := client.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	res, err := client.CallTool(ctx, "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "hi" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestClient_ContentLengthCeiling(t *testing.T) {
	pr, pw := io.Pipe()
	client := NewClient("dummy", nil, nil)
	client.stdout = pr

	go client.readLoop()

	_, _ = pw.Write([]byte("Content-Length: 99999999999\r\n\r\n"))

	select {
	case <-client.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not drop connection after oversized Content-Length")
	}
	pw.Close()
}

func TestClient_RequestTimeout(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	go io.Copy(io.Discard, stdinR)

	stdoutR, stdoutW := io.Pipe()
	_ = stdoutW

	client := NewClient("dummy", nil, nil)
	client.stdin = stdinW
	client.stdout = stdoutR
	client.requestTimeout = 200 * time.Millisecond

	go client.readLoop()

	_, err := client.Call(context.Background(), "initialize", map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected deadline error, got %v", err)
	}
	stdinW.Close()
	stdoutW.Close()
}
