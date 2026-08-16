package mcp

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/tools"
)

// Manager coordinates all configured MCP clients.
type Manager struct {
	servers map[string]config.MCPServerConfig
	clients map[string]*Client
	mu      sync.RWMutex
}

// NewManager creates a new MCP Manager with server configurations.
func NewManager(servers map[string]config.MCPServerConfig) *Manager {
	if servers == nil {
		servers = make(map[string]config.MCPServerConfig)
	}
	return &Manager{
		servers: servers,
		clients: make(map[string]*Client),
	}
}

// StartAll initializes all configured MCP servers and registers their tools.
func (m *Manager) StartAll(ctx context.Context, toolRegistry *tools.Registry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, cfg := range m.servers {
		if cfg.Command == "" {
			continue
		}

		client := NewClient(cfg.Command, cfg.Args, cfg.Env)
		if err := client.Start(); err != nil {
			slog.Warn("failed to start MCP server", "name", name, "error", err)
			continue
		}

		initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := client.Initialize(initCtx); err != nil {
			cancel()
			_ = client.Close()
			slog.Warn("failed to initialize MCP server", "name", name, "error", err)
			continue
		}

		toolList, err := client.ListTools(initCtx)
		cancel()

		if err != nil {
			_ = client.Close()
			slog.Warn("failed to list tools for MCP server", "name", name, "error", err)
			continue
		}

		m.clients[name] = client

		for _, tInfo := range toolList {
			mcpTool := NewMCPTool(name, tInfo, client)
			toolRegistry.Register(mcpTool)
			slog.Debug("registered MCP tool", "server", name, "tool", mcpTool.Spec().Name)
		}
	}

	return nil
}

// CloseAll closes all active MCP client connections.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			slog.Warn("error closing MCP client", "name", name, "error", err)
		}
	}
	m.clients = make(map[string]*Client)
}

// AddServer saves a new server configuration.
func (m *Manager) AddServer(name string, cfg config.MCPServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[name] = cfg
}

// RemoveServer removes a server configuration.
func (m *Manager) RemoveServer(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if client, ok := m.clients[name]; ok {
		_ = client.Close()
		delete(m.clients, name)
	}
	delete(m.servers, name)
}

// ListServers returns all configured server names and configs.
func (m *Manager) ListServers() map[string]config.MCPServerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	copyMap := make(map[string]config.MCPServerConfig, len(m.servers))
	for k, v := range m.servers {
		copyMap[k] = v
	}
	return copyMap
}
