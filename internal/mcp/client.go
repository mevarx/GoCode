package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// JSONRPCRequest represents a standard JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents a standard JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError holds JSON-RPC error payload.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// ToolInfo describes an MCP tool definition.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolContent is a piece of output returned from an MCP tool call.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallToolResult holds the result of a tools/call request.
type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError"`
}

// Client manages an MCP server process communicating via stdio JSON-RPC.
type Client struct {
	command string
	args    []string
	env     map[string]string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	seq     uint64
	pending map[uint64]chan *JSONRPCResponse
	mu      sync.Mutex
	closed  bool
	doneCh  chan struct{}
}

// NewClient creates a new MCP client.
func NewClient(command string, args []string, env map[string]string) *Client {
	return &Client{
		command: command,
		args:    args,
		env:     env,
		pending: make(map[uint64]chan *JSONRPCResponse),
		doneCh:  make(chan struct{}),
	}
}

// Start launches the MCP server process.
func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cmd = exec.Command(c.command, c.args...)

	if len(c.env) > 0 {
		c.cmd.Env = os.Environ()
		for k, v := range c.env {
			c.cmd.Env = append(c.cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	c.stdin = stdin

	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	c.stdout = stdout

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start MCP server %s: %w", c.command, err)
	}

	go c.readLoop()

	return nil
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			slog.Debug("mcp client received non-jsonrpc line", "line", string(line))
			continue
		}

		if resp.ID != nil {
			c.mu.Lock()
			ch, ok := c.pending[*resp.ID]
			if ok {
				delete(c.pending, *resp.ID)
			}
			c.mu.Unlock()

			if ok {
				ch <- &resp
			}
		}
	}

	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.doneCh)
	}
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// Call sends a JSON-RPC request and awaits the response.
func (c *Client) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddUint64(&c.seq, 1)

	var paramsJSON json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
		paramsJSON = b
	}

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  paramsJSON,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	ch := make(chan *JSONRPCResponse, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp client is closed")
	}
	c.pending[id] = ch
	_, writeErr := c.stdin.Write(reqBytes)
	c.mu.Unlock()

	if writeErr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to write to mcp stdin: %w", writeErr)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.doneCh:
		return nil, fmt.Errorf("mcp server process exited")
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, fmt.Errorf("no response received from mcp server")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(method string, params interface{}) error {
	var paramsJSON json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("failed to marshal params: %w", err)
		}
		paramsJSON = b
	}

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mcp client is closed")
	}

	_, err = c.stdin.Write(reqBytes)
	return err
}

// Initialize performs the MCP protocol handshake.
func (c *Client) Initialize(ctx context.Context) error {
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "gocode",
			"version": "0.2.0",
		},
	}

	if _, err := c.Call(ctx, "initialize", initParams); err != nil {
		return fmt.Errorf("mcp initialize failed: %w", err)
	}

	if err := c.Notify("notifications/initialized", map[string]interface{}{}); err != nil {
		return fmt.Errorf("mcp initialized notification failed: %w", err)
	}

	return nil
}

// ListTools queries the available tools from the MCP server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	resBytes, err := c.Call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("tools/list failed: %w", err)
	}

	var res struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(resBytes, &res); err != nil {
		return nil, fmt.Errorf("failed to parse tools/list result: %w", err)
	}

	return res.Tools, nil
}

// CallTool executes a specific tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	var argsObj interface{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argsObj)
	}

	params := map[string]interface{}{
		"name":      name,
		"arguments": argsObj,
	}

	resBytes, err := c.Call(ctx, "tools/call", params)
	if err != nil {
		return CallToolResult{IsError: true}, err
	}

	var result CallToolResult
	if err := json.Unmarshal(resBytes, &result); err != nil {
		return CallToolResult{IsError: true}, fmt.Errorf("failed to parse tools/call result: %w", err)
	}

	return result, nil
}

// Close gracefully terminates the child process.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}

	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}

	return nil
}
