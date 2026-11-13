package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/version"
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
			if path := os.Getenv("GO_FAKE_MCP_PARAMS_FILE"); path != "" {
				_ = os.WriteFile(path, req.Params, 0o600)
			}
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
	client.SetRequestTimeout(200 * time.Millisecond)

	go client.readLoop()

	_, err := client.Call(context.Background(), "initialize", map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected deadline error, got %v", err)
	}
	stdinW.Close()
	stdoutW.Close()
}

// TestClient_ReadLoop_NonHeaderCLineIsMessage guards framing detection: a
// line that merely starts with 'C' must not be swallowed as a header.
func TestClient_ReadLoop_NonHeaderCLineIsMessage(t *testing.T) {
	client := NewClient("dummy", nil, nil)

	respCh := make(chan *JSONRPCResponse, 1)
	client.pending[1] = respCh

	input := "Connecting...\n" + `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"
	client.readLoopFrom(bufio.NewReader(strings.NewReader(input)))

	resp, ok := <-respCh
	if !ok || resp == nil {
		t.Fatal("expected the line beginning with 'C' to be handled as a JSON-RPC message")
	}
	if resp.ID == nil || *resp.ID != 1 {
		t.Fatalf("expected response for id 1, got %+v", resp)
	}
	if string(resp.Result) != `{}` {
		t.Fatalf("expected empty object result, got %s", resp.Result)
	}
}

// capturedInitializeVersion starts the fake server, runs the handshake and
// returns the clientInfo.version it received.
func capturedInitializeVersion(t *testing.T) string {
	t.Helper()

	paramsFile := filepath.Join(t.TempDir(), "initialize.json")
	env := map[string]string{
		"GO_FAKE_MCP_SERVER":      "1",
		"GO_FAKE_MCP_PARAMS_FILE": paramsFile,
	}
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

	data, err := os.ReadFile(paramsFile)
	if err != nil {
		t.Fatalf("read captured initialize params: %v", err)
	}

	var params struct {
		ClientInfo struct {
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(data, &params); err != nil {
		t.Fatalf("parse captured initialize params: %v", err)
	}
	return params.ClientInfo.Version
}

func TestClient_InitializeAdvertisesPackageVersion(t *testing.T) {
	if got := capturedInitializeVersion(t); got != version.Version {
		t.Fatalf("expected advertised version %q, got %q", version.Version, got)
	}
}

func TestClient_InitializeAdvertisesVersionVariable(t *testing.T) {
	orig := version.Version
	version.Version = "9.9.9-test"
	defer func() { version.Version = orig }()

	if got := capturedInitializeVersion(t); got != "9.9.9-test" {
		t.Fatalf("expected advertised version to follow version.Version, got %q", got)
	}
}
