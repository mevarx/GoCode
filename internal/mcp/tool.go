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
	name := info.Name
	if serverName != "" && !strings.HasPrefix(name, serverName+"_") {
		name = fmt.Sprintf("%s_%s", serverName, info.Name)
	}

	return &MCPTool{
		serverName:  serverName,
		toolName:    info.Name,
		description: info.Description,
		parameters:  info.InputSchema,
		client:      client,
	}
}

func (m *MCPTool) Spec() tools.ToolSpec {
	name := m.toolName
	if m.serverName != "" {
		name = fmt.Sprintf("%s_%s", m.serverName, m.toolName)
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
