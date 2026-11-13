package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/tools"
)

// fakeServerConfig builds a config that spawns the current test binary as the
// named fake MCP server.
func fakeServerConfig(testName string, env map[string]string, timeoutSeconds int) config.MCPServerConfig {
	return config.MCPServerConfig{
		Command:               os.Args[0],
		Args:                  []string{"-test.run=" + testName},
		Env:                   env,
		RequestTimeoutSeconds: timeoutSeconds,
	}
}

// newFakeManager starts the standard fake server and returns the manager and
// registry with its tools.
func newFakeManager(t *testing.T, timeoutSeconds int) (*Manager, *tools.Registry) {
	t.Helper()

	mgr := NewManager(map[string]config.MCPServerConfig{
		"fake": fakeServerConfig("TestFakeMCPServerProcess", map[string]string{
			"GO_FAKE_MCP_SERVER": "1",
		}, timeoutSeconds),
	})
	reg := tools.NewRegistry()
	if err := mgr.StartAll(context.Background(), reg); err != nil {
		mgr.CloseAll()
		t.Fatalf("start all: %v", err)
	}
	t.Cleanup(mgr.CloseAll)
	return mgr, reg
}

func currentClient(t *testing.T, mgr *Manager, server string) *Client {
	t.Helper()
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	client, ok := mgr.clients[server]
	if !ok || client == nil {
		t.Fatalf("no client registered for server %q", server)
	}
	return client
}

// killServer simulates a crash by killing the child process and waiting for the
// client to observe EOF.
func killServer(t *testing.T, client *Client) {
	t.Helper()
	if err := client.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill fake server: %v", err)
	}
	select {
	case <-client.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("client did not notice the server process exit")
	}
	if !client.IsClosed() {
		t.Fatal("expected client to report closed after the process died")
	}
}

func TestManager_RestartsDeadServerOnDemand(t *testing.T) {
	mgr, reg := newFakeManager(t, 10)

	client := currentClient(t, mgr, "fake")
	oldPid := client.cmd.Process.Pid
	killServer(t, client)

	before := len(reg.List())
	tool := reg.Get("mcp_fake_echo")
	if tool == nil {
		t.Fatalf("expected mcp_fake_echo to stay registered, have %v", reg.List())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := tool.Execute(ctx, json.RawMessage(`{"msg":"after-restart"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("execute after restart returned error result: %s", res.Error)
	}
	if res.Output != "hi" {
		t.Fatalf("expected fake server output %q, got %q", "hi", res.Output)
	}

	restarted := currentClient(t, mgr, "fake")
	if restarted == client {
		t.Fatal("expected the dead client to be replaced")
	}
	if restarted.IsClosed() {
		t.Fatal("expected the restarted client to be live")
	}
	if restarted.cmd.Process.Pid == oldPid {
		t.Fatalf("expected a new server process, still PID %d", oldPid)
	}

	if got := len(reg.List()); got != before {
		t.Fatalf("restart must not re-register tools: registry went from %d to %d entries", before, got)
	}
}

func TestManager_RestartFailureReturnsClearError(t *testing.T) {
	mgr, reg := newFakeManager(t, 10)

	client := currentClient(t, mgr, "fake")
	killServer(t, client)

	// Point the server at a command that cannot be started so the restart fails.
	mgr.AddServer("fake", config.MCPServerConfig{
		Command: "gocode-mcp-command-that-does-not-exist-8f2a",
	})

	tool := reg.Get("mcp_fake_echo")
	if tool == nil {
		t.Fatalf("expected mcp_fake_echo to stay registered, have %v", reg.List())
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected an error result when the server cannot be restarted")
	}
	if !strings.Contains(res.Error, `MCP server "fake" unavailable`) {
		t.Fatalf("expected a clear unavailable error, got %q", res.Error)
	}
	if got := len(reg.List()); got != 1 {
		t.Fatalf("failed restart must not change the registry, have %v", reg.List())
	}
}

func TestManager_ConcurrentRestartIsSingleFlight(t *testing.T) {
	mgr, reg := newFakeManager(t, 10)

	client := currentClient(t, mgr, "fake")
	killServer(t, client)

	tool := reg.Get("mcp_fake_echo")
	if tool == nil {
		t.Fatalf("expected mcp_fake_echo to stay registered, have %v", reg.List())
	}

	const workers = 5
	var wg sync.WaitGroup
	failures := make(chan string, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				failures <- err.Error()
				return
			}
			if res.Error != "" {
				failures <- res.Error
			}
		}()
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Errorf("concurrent execute failed: %s", msg)
	}

	if got := len(reg.List()); got != 1 {
		t.Fatalf("concurrent restarts must not duplicate registration, have %v", reg.List())
	}
	restarted := currentClient(t, mgr, "fake")
	if restarted == client {
		t.Fatal("expected the dead client to be replaced")
	}
}

func TestManager_EnsureClientRefusesAfterCloseAll(t *testing.T) {
	mgr, _ := newFakeManager(t, 10)
	mgr.CloseAll()

	if _, err := mgr.EnsureClient(context.Background(), "fake"); err == nil ||
		!strings.Contains(err.Error(), "manager is closed") {
		t.Fatalf("expected manager-is-closed error, got %v", err)
	}
}

func TestManager_RequestTimeoutFromConfig(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{name: "explicit second", seconds: 1, want: time.Second},
		{name: "zero uses default", seconds: 0, want: 60 * time.Second},
		{name: "negative uses default", seconds: -5, want: 60 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fakeServerConfig("TestFakeMCPServerProcess", map[string]string{
				"GO_FAKE_MCP_SERVER": "1",
			}, tc.seconds)
			client := NewManager(nil).newClient(cfg)
			if got := client.requestTimeout; got != tc.want {
				t.Fatalf("expected request timeout %s, got %s", tc.want, got)
			}
		})
	}
}

// TestManager_StartAllAppliesRequestTimeout proves the configured timeout
// reaches the client that StartAll actually spawns.
func TestManager_StartAllAppliesRequestTimeout(t *testing.T) {
	mgr, _ := newFakeManager(t, 7)

	client := currentClient(t, mgr, "fake")
	if got := client.requestTimeout; got != 7*time.Second {
		t.Fatalf("expected started client request timeout 7s, got %s", got)
	}
}

func TestClient_DoneAndIsClosed(t *testing.T) {
	client := NewClient(os.Args[0], []string{"-test.run=TestFakeMCPServerProcess"}, map[string]string{
		"GO_FAKE_MCP_SERVER": "1",
	})
	if err := client.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-client.Done():
		t.Fatal("Done must not be closed for a running client")
	default:
	}
	if client.IsClosed() {
		t.Fatal("IsClosed must be false for a running client")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done was not closed by Close")
	}
	if !client.IsClosed() {
		t.Fatal("IsClosed must be true after Close")
	}
}

// TestFakeMCPCapsServer is the child process used by the cap tests. It
// advertises a configurable tool list.
func TestFakeMCPCapsServer(t *testing.T) {
	if os.Getenv("GO_FAKE_MCP_CAPS_SERVER") != "1" {
		return
	}

	count, _ := strconv.Atoi(os.Getenv("GO_FAKE_MCP_TOOL_COUNT"))
	payload := buildCapsToolList(
		count,
		os.Getenv("GO_FAKE_MCP_FAT_DESCRIPTION") == "1",
		os.Getenv("GO_FAKE_MCP_BIG_SCHEMA") == "1",
		os.Getenv("GO_FAKE_MCP_LONG_NAME") == "1",
	)

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req JSONRPCRequest
		if json.Unmarshal([]byte(line), &req) != nil || req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"caps","version":"0.0.1"}}}`+"\n", *req.ID)
		case "tools/list":
			fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"tools":%s}}`+"\n", *req.ID, payload)
		case "tools/call":
			fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`+"\n", *req.ID)
		}
	}
	os.Exit(0)
}

func buildCapsToolList(count int, fatDescription, bigSchema, longName bool) string {
	list := make([]ToolInfo, 0, count+3)
	for i := range count {
		list = append(list, ToolInfo{
			Name:        fmt.Sprintf("tool_%d", i),
			Description: fmt.Sprintf("tool %d", i),
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}
	if fatDescription {
		list = append(list, ToolInfo{
			Name:        "fat",
			Description: strings.Repeat("d", 9*1024),
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}
	if bigSchema {
		list = append(list, ToolInfo{
			Name:        "wide",
			InputSchema: json.RawMessage(`{"type":"object","description":"` + strings.Repeat("s", 33*1024) + `"}`),
		})
	}
	if longName {
		list = append(list, ToolInfo{
			Name:        "long_" + strings.Repeat("n", maxToolNameLen),
			Description: "name too long",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}
	encoded, _ := json.Marshal(list)
	return string(encoded)
}

func newCapsManager(t *testing.T, env map[string]string) *Manager {
	t.Helper()
	base := map[string]string{"GO_FAKE_MCP_CAPS_SERVER": "1"}
	for k, v := range env {
		base[k] = v
	}
	return NewManager(map[string]config.MCPServerConfig{
		"caps": fakeServerConfig("TestFakeMCPCapsServer", base, 10),
	})
}

func TestManager_RejectsServerOverToolCountCap(t *testing.T) {
	mgr := newCapsManager(t, map[string]string{"GO_FAKE_MCP_TOOL_COUNT": "201"})
	reg := tools.NewRegistry()
	err := mgr.StartAll(context.Background(), reg)
	defer mgr.CloseAll()

	if err == nil {
		t.Fatal("expected an error for a server advertising more than the tool cap")
	}
	if !strings.Contains(err.Error(), "exceeding the cap of 200") {
		t.Fatalf("expected the error to mention the tool count cap, got %v", err)
	}
	if !strings.Contains(err.Error(), "201") {
		t.Fatalf("expected the error to include the advertised count, got %v", err)
	}
	if got := len(reg.List()); got != 0 {
		t.Fatalf("expected no tools registered from an oversized server, got %d", got)
	}
}

func TestManager_SkipsOversizedToolsOnly(t *testing.T) {
	mgr := newCapsManager(t, map[string]string{
		"GO_FAKE_MCP_TOOL_COUNT":      "2",
		"GO_FAKE_MCP_FAT_DESCRIPTION": "1",
		"GO_FAKE_MCP_BIG_SCHEMA":      "1",
		"GO_FAKE_MCP_LONG_NAME":       "1",
	})
	reg := tools.NewRegistry()
	err := mgr.StartAll(context.Background(), reg)
	defer mgr.CloseAll()

	if err == nil {
		t.Fatal("expected an error summary for skipped oversized tools")
	}
	if !strings.Contains(err.Error(), "skipped by name/size caps") {
		t.Fatalf("expected the summary to mention skipped tools, got %v", err)
	}

	names := reg.List()
	if len(names) != 2 {
		t.Fatalf("expected only the 2 valid tools to register, got %v", names)
	}
	for _, want := range []string{"mcp_caps_tool_0", "mcp_caps_tool_1"} {
		if reg.Get(want) == nil {
			t.Fatalf("expected %q to register, have %v", want, names)
		}
	}
	for _, unwanted := range []string{"mcp_caps_fat", "mcp_caps_wide"} {
		if reg.Get(unwanted) != nil {
			t.Fatalf("expected %q to be rejected by caps", unwanted)
		}
	}
	for _, name := range names {
		if strings.Contains(name, "long_") {
			t.Fatalf("expected the over-long tool name to be rejected, got %v", names)
		}
	}
}

func TestMCPTool_ImplementsPreviewer(t *testing.T) {
	mcpTool := NewMCPTool("weather_srv", ToolInfo{Name: "get_weather", Description: "Get weather"}, nil)
	var previewer tools.Previewer = mcpTool

	preview, err := previewer.Preview(context.Background(), json.RawMessage(`{"location":"Paris","days":2}`))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.Description, "MCP tool weather_srv/get_weather") {
		t.Fatalf("expected server/tool name in preview, got %q", preview.Description)
	}
	if !strings.Contains(preview.Description, "Arguments:") {
		t.Fatalf("expected arguments header in preview, got %q", preview.Description)
	}
	if !strings.Contains(preview.Description, "\n  \"location\": \"Paris\"") {
		t.Fatalf("expected indented JSON arguments in preview, got %q", preview.Description)
	}
	if preview.Diff != "" || preview.Command != "" {
		t.Fatalf("MCP previews should not carry diff/command, got %+v", preview)
	}
}

func TestMCPTool_PreviewInvalidJSONFallsBackToRaw(t *testing.T) {
	mcpTool := NewMCPTool("weather_srv", ToolInfo{Name: "get_weather"}, nil)

	preview, err := mcpTool.Preview(context.Background(), json.RawMessage(`not-json`))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.Description, "not-json") {
		t.Fatalf("expected raw arguments on invalid JSON, got %q", preview.Description)
	}

	empty, err := mcpTool.Preview(context.Background(), nil)
	if err != nil {
		t.Fatalf("preview empty: %v", err)
	}
	if !strings.Contains(empty.Description, "{}") {
		t.Fatalf("expected empty object for missing arguments, got %q", empty.Description)
	}
}
