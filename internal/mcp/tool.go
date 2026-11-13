package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/mevarx/GoCode/internal/tools"
)

// MCPTool adapts an MCP tool to the tools.Tool interface.
type MCPTool struct {
	serverName  string
	toolName    string
	description string
	parameters  json.RawMessage

	// manager is nil for adapters built without one (unit tests); when set it
	// is asked for a live client after the current one dies.
	manager *Manager
	// client is swapped atomically on restart so concurrent executions never
	// observe a half-updated pointer.
	client atomic.Pointer[Client]
}

// NewMCPTool creates a tools.Tool adapter for an MCP tool.
func NewMCPTool(serverName string, info ToolInfo, client *Client) *MCPTool {
	t := &MCPTool{
		serverName:  serverName,
		toolName:    info.Name,
		description: info.Description,
		parameters:  info.InputSchema,
	}
	t.client.Store(client)
	return t
}

// newManagedMCPTool creates an adapter that can restart its server on demand.
func newManagedMCPTool(manager *Manager, serverName string, info ToolInfo, client *Client) *MCPTool {
	t := NewMCPTool(serverName, info, client)
	t.manager = manager
	return t
}

func (m *MCPTool) Spec() tools.ToolSpec {
	// Guard the prefix, or a server that already namespaces its tools
	// yields "srv_srv_foo".
	name := m.toolName
	if m.serverName != "" {
		if !strings.HasPrefix(name, m.serverName+"_") {
			name = fmt.Sprintf("%s_%s", m.serverName, m.toolName)
		}
	}
	// Reserved prefix: an MCP server can never shadow a built-in tool name.
	if !strings.HasPrefix(name, "mcp_") {
		name = "mcp_" + name
	}
	return tools.ToolSpec{
		Name:        name,
		Description: fmt.Sprintf("[MCP: %s] %s", m.serverName, m.description),
		Parameters:  m.parameters,
	}
}

func (m *MCPTool) RequiresApproval() bool {
	return true
}

// Preview shows the server, tool, and arguments awaiting approval.
func (m *MCPTool) Preview(_ context.Context, args json.RawMessage) (tools.Preview, error) {
	return tools.Preview{
		Description: fmt.Sprintf("MCP tool %s/%s\nArguments:\n%s", m.serverName, m.toolName, indentArgs(args)),
	}, nil
}

// indentArgs pretty-prints tool arguments, falling back to the raw payload
// when it is not valid JSON.
func indentArgs(args json.RawMessage) string {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return string(trimmed)
	}
	return buf.String()
}

func (m *MCPTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	client := m.client.Load()
	if client == nil {
		return tools.Result{Error: fmt.Sprintf("MCP server %q unavailable: client is not initialized", m.serverName)}, nil
	}

	callRes, err := client.CallTool(ctx, m.toolName, args)
	if err != nil && m.manager != nil && client.IsClosed() && ctx.Err() == nil {
		// The server process died: restart it and retry exactly once.
		restarted, restartErr := m.manager.EnsureClient(ctx, m.serverName)
		if restartErr != nil {
			return tools.Result{Error: restartErr.Error()}, nil
		}
		m.client.Store(restarted)
		callRes, err = restarted.CallTool(ctx, m.toolName, args)
	}
	if err != nil {
		return tools.Result{Error: err.Error()}, nil
	}

	var output strings.Builder
	for i, c := range callRes.Content {
		if i > 0 {
			output.WriteString("\n")
		}
		output.WriteString(c.Text)
	}

	if callRes.IsError {
		return tools.Result{Error: output.String()}, nil
	}

	return tools.Result{Output: output.String()}, nil
}
