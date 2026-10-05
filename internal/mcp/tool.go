package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/tools"
)

// MCPTool adapts an MCP tool to the tools.Tool interface.
type MCPTool struct {
	serverName  string
	toolName    string
	description string
	parameters  json.RawMessage
	client      *Client
}

// NewMCPTool creates a tools.Tool adapter for an MCP tool.
func NewMCPTool(serverName string, info ToolInfo, client *Client) *MCPTool {
	return &MCPTool{
		serverName:  serverName,
		toolName:    info.Name,
		description: info.Description,
		parameters:  info.InputSchema,
		client:      client,
	}
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

func (m *MCPTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	callRes, err := m.client.CallTool(ctx, m.toolName, args)
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
