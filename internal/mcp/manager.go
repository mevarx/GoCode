package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/tools"
)

// Caps on what one MCP server may contribute to the tool registry. They bound
// context flooding: a hostile or broken server must not be able to inflate
// every prompt with unbounded tool metadata.
const (
	maxToolsPerServer      = 200
	maxToolNameLen         = 128
	maxToolDescriptionSize = 8 * 1024
	maxToolSchemaSize      = 32 * 1024
)

// initTimeout bounds the MCP handshake during start and restart.
const initTimeout = 10 * time.Second

// Manager coordinates all configured MCP clients.
type Manager struct {
	servers map[string]config.MCPServerConfig
	clients map[string]*Client
	// EnvScrub lists additional env var names or patterns to drop from every
	// spawned MCP server, on top of the built-in credential patterns.
	EnvScrub []string
	// mu guards servers, clients, and closed. It is also held across a server
	// restart so CloseAll cannot race a process respawn.
	mu     sync.RWMutex
	closed bool
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

// newClient builds an unstarted client for cfg with its request timeout applied.
func (m *Manager) newClient(cfg config.MCPServerConfig) *Client {
	client := NewClientWithEnvScrub(cfg.Command, cfg.Args, cfg.Env, m.EnvScrub)
	client.SetRequestTimeout(time.Duration(cfg.RequestTimeoutSeconds) * time.Second)
	return client
}

// startClient spawns the server process and performs the MCP handshake.
func (m *Manager) startClient(ctx context.Context, name string, cfg config.MCPServerConfig) (*Client, error) {
	client := m.newClient(cfg)

	if err := client.Start(); err != nil {
		return nil, fmt.Errorf("failed to start MCP server %q: %w", name, err)
	}

	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	if err := client.Initialize(initCtx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to initialize MCP server %q: %w", name, err)
	}

	return client, nil
}

// validateToolInfo enforces the per-tool caps; the returned error explains
// which cap was exceeded so the caller can log and skip that tool only.
func validateToolInfo(tInfo ToolInfo) error {
	if len(tInfo.Name) > maxToolNameLen {
		return fmt.Errorf("tool name length %d exceeds cap of %d bytes", len(tInfo.Name), maxToolNameLen)
	}
	if len(tInfo.Description) > maxToolDescriptionSize {
		return fmt.Errorf("tool description size %d exceeds cap of %d bytes", len(tInfo.Description), maxToolDescriptionSize)
	}
	if len(tInfo.InputSchema) > maxToolSchemaSize {
		return fmt.Errorf("tool input schema size %d exceeds cap of %d bytes", len(tInfo.InputSchema), maxToolSchemaSize)
	}
	return nil
}

// StartAll initializes all configured MCP servers and registers their tools.
func (m *Manager) StartAll(ctx context.Context, toolRegistry *tools.Registry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var failures []error

	for name, cfg := range m.servers {
		if cfg.Command == "" {
			continue
		}

		client, err := m.startClient(ctx, name, cfg)
		if err != nil {
			slog.Warn("failed to start MCP server", "name", name, "error", err)
			fmt.Fprintf(os.Stderr, "gocode: %v\n", err)
			failures = append(failures, err)
			continue
		}

		listCtx, cancel := context.WithTimeout(ctx, initTimeout)
		toolList, err := client.ListTools(listCtx)
		cancel()

		if err != nil {
			_ = client.Close()
			slog.Warn("failed to list tools for MCP server", "name", name, "error", err)
			fmt.Fprintf(os.Stderr, "gocode: MCP server %q failed to list tools: %v\n", name, err)
			failures = append(failures, fmt.Errorf("MCP server %q: %w", name, err))
			continue
		}

		if len(toolList) > maxToolsPerServer {
			_ = client.Close()
			err := fmt.Errorf("MCP server %q advertises %d tools, exceeding the cap of %d; no tools registered", name, len(toolList), maxToolsPerServer)
			slog.Warn("MCP server exceeds tool count cap", "name", name, "tools", len(toolList), "cap", maxToolsPerServer)
			fmt.Fprintf(os.Stderr, "gocode: %v\n", err)
			failures = append(failures, err)
			continue
		}

		m.clients[name] = client

		skipped := 0
		for _, tInfo := range toolList {
			if err := validateToolInfo(tInfo); err != nil {
				skipped++
				slog.Warn("skipping oversized MCP tool", "server", name, "tool", tInfo.Name, "error", err)
				fmt.Fprintf(os.Stderr, "gocode: skipping MCP tool %q from %q: %v\n", tInfo.Name, name, err)
				continue
			}

			mcpTool := newManagedMCPTool(m, name, tInfo, client)
			if err := toolRegistry.Register(mcpTool); err != nil {
				slog.Warn("failed to register MCP tool", "server", name, "tool", tInfo.Name, "error", err)
				fmt.Fprintf(os.Stderr, "gocode: could not register MCP tool from %q: %v\n", name, err)
				failures = append(failures, err)
				continue
			}
			slog.Debug("registered MCP tool", "server", name, "tool", mcpTool.Spec().Name)
		}

		if skipped > 0 {
			failures = append(failures, fmt.Errorf("MCP server %q: %d of %d tools skipped by name/size caps", name, skipped, len(toolList)))
		}
	}

	return errors.Join(failures...)
}

// EnsureClient returns a live client for server, restarting the server process
// when its previous client is closed. Concurrent callers share a single
// restart, and the restart is serialized against CloseAll. Tools stay
// registered across a restart; only the client pointer moves.
func (m *Manager) EnsureClient(ctx context.Context, server string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, fmt.Errorf("MCP server %q unavailable: manager is closed", server)
	}

	if client, ok := m.clients[server]; ok && !client.IsClosed() {
		return client, nil
	}

	cfg, ok := m.servers[server]
	if !ok || cfg.Command == "" {
		return nil, fmt.Errorf("MCP server %q unavailable: server is not configured", server)
	}

	slog.Info("restarting MCP server", "name", server)
	client, err := m.startClient(ctx, server, cfg)
	if err != nil {
		return nil, fmt.Errorf("MCP server %q unavailable: %w", server, err)
	}

	m.clients[server] = client
	return client, nil
}

// CloseAll closes all active MCP client connections. After CloseAll, servers
// are never restarted on demand.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
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
