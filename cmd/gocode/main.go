package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/ignore"
	"github.com/mevarx/GoCode/internal/mcp"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
	"github.com/mevarx/GoCode/internal/tools"
	"github.com/mevarx/GoCode/internal/tui"
)

var (
	flagProvider  string
	flagModel     string
	flagConfig    string
	flagTUI       bool
	flagVerbose   bool
	flagNew       bool
	flagSessionID string
	flagTail      int
	flagFollow    bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "gocode",
		Short: "GoCode — Terminal coding agent",
		Long:  "A Go-native terminal coding agent. Local-first, provider-agnostic, approval-gated.",
		RunE:  runAgent,
	}

	rootCmd.Version = "0.2.0"

	rootCmd.Flags().StringVar(&flagProvider, "provider", "", "LLM provider to use")
	rootCmd.Flags().StringVar(&flagModel, "model", "", "Model to use")
	rootCmd.Flags().StringVar(&flagConfig, "config", "", "Path to config file")
	rootCmd.Flags().BoolVar(&flagTUI, "tui", true, "Enable rich TUI (set --tui=false for plain mode)")
	rootCmd.Flags().BoolVar(&flagNew, "new", false, "Start a new session instead of auto-resuming")
	rootCmd.Flags().StringVar(&flagSessionID, "session", "", "Resume a specific session by ID")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Enable debug logging")

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check health and reachability of configured AI providers",
		RunE:  runDoctor,
	}
	rootCmd.AddCommand(doctorCmd)

	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage Model Context Protocol (MCP) servers",
	}

	mcpAddCmd := &cobra.Command{
		Use:   "add <name> <command> [args...]",
		Short: "Add or update an MCP server configuration",
		Args:  cobra.MinimumNArgs(2),
		RunE:  runMCPAdd,
	}

	mcpListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all configured MCP servers",
		RunE:  runMCPList,
	}

	mcpRemoveCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an MCP server configuration",
		Args:  cobra.ExactArgs(1),
		RunE:  runMCPRemove,
	}

	mcpCmd.AddCommand(mcpAddCmd, mcpListCmd, mcpRemoveCmd)
	rootCmd.AddCommand(mcpCmd)

	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "View or follow GoCode application logs",
		RunE:  runLogs,
	}
	logsCmd.Flags().IntVarP(&flagTail, "tail", "n", 50, "Number of lines to show from the end of the log")
	logsCmd.Flags().BoolVarP(&flagFollow, "follow", "f", false, "Follow log output continuously")
	rootCmd.AddCommand(logsCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var gatewayProviderNames = []string{"omniroute", "openai", "gemini", "groq", "openrouter", "qwen", "kimi"}

func gatewayConfigFor(cfg config.ProviderConfig, name string) config.GatewayConfig {
	switch name {
	case "omniroute":
		return cfg.OmniRoute
	case "openai":
		return cfg.OpenAI
	case "gemini":
		return cfg.Gemini
	case "groq":
		return cfg.Groq
	case "openrouter":
		return cfg.OpenRouter
	case "qwen":
		return cfg.Qwen
	case "kimi":
		return cfg.Kimi
	default:
		return config.GatewayConfig{}
	}
}

// setupLogging configures structured logging and returns a cleanup function.
func setupLogging(verbose bool) (cleanup func(), err error) {
	logFilePath := config.LogFilePath()
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0o755); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	var writer io.Writer = logFile
	if verbose {
		writer = io.MultiWriter(os.Stderr, logFile)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})))
	return func() { logFile.Close() }, nil
}

func runAgent(cmd *cobra.Command, args []string) error {
	if err := config.EnsureDirs(); err != nil {
		return fmt.Errorf("failed to create data dirs: %w", err)
	}

	logCleanup, err := setupLogging(flagVerbose)
	if err != nil {
		slog.Warn("failed to initialize file logging", "error", err)
	} else {
		defer logCleanup()
	}

	var cfg config.Config
	if flagConfig != "" {
		cfg, err = config.LoadFromPath(flagConfig)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	providerRegistry := provider.NewRegistry()

	ollamaProvider, err := provider.NewOllamaProvider(cfg.Provider.Ollama.Host)
	if err != nil {
		return fmt.Errorf("failed to initialize ollama provider: %w", err)
	}
	providerRegistry.Register(ollamaProvider)

	for _, name := range gatewayProviderNames {
		gCfg := gatewayConfigFor(cfg.Provider, name)
		providerRegistry.Register(provider.NewGatewayProxyProvider(name, gCfg))
	}

	providerRegistry.Register(provider.NewAnthropicProvider(cfg.Provider.Anthropic))

	storePath := filepath.Join(config.SessionDir(), "sessions.db")
	sessionStore, err := session.NewStore(storePath)
	if err != nil {
		slog.Warn("failed to open session store", "path", storePath, "error", err)
	}
	if sessionStore != nil {
		defer sessionStore.Close()
	}

	var activeSessionRec *session.SessionRecord
	if sessionStore != nil && !flagNew {
		if flagSessionID != "" {
			activeSessionRec, _ = sessionStore.GetSession(flagSessionID)
			if activeSessionRec == nil {
				slog.Info("session not found, creating new", "id", flagSessionID)
			}
		} else {
			activeSessionRec, _ = sessionStore.GetLastSession()
		}
	}

	providerName := cfg.Provider.Default
	if flagProvider != "" {
		providerName = flagProvider
	} else if activeSessionRec != nil && activeSessionRec.Provider != "" {
		providerName = activeSessionRec.Provider
	}

	if err := providerRegistry.Switch(providerName); err != nil {
		return fmt.Errorf("failed to select provider: %w", err)
	}

	var model string
	if flagModel != "" {
		model = flagModel
	} else if activeSessionRec != nil && activeSessionRec.Model != "" {
		model = activeSessionRec.Model
	} else {
		switch providerName {
		case "ollama":
			model = cfg.Provider.Ollama.DefaultModel
			if model == "" {
				if models, err := ollamaProvider.Models(context.Background()); err == nil && len(models) > 0 {
					model = models[0]
				} else {
					model = "codellama"
				}
			}
		case "anthropic":
			model = cfg.Provider.Anthropic.DefaultModel
		default:
			gCfg := gatewayConfigFor(cfg.Provider, providerName)
			model = gCfg.DefaultModel
		}
	}

	var sess *agent.Session
	if activeSessionRec != nil {
		sess = agent.NewSessionWithStore(activeSessionRec.ID, providerName, model, sessionStore)
		sess.LoadMessages(activeSessionRec.Messages)
		slog.Info("resumed session", "id", activeSessionRec.ID, "messages", len(activeSessionRec.Messages))
	} else {
		newID := session.GenerateID()
		if flagSessionID != "" {
			newID = flagSessionID
		}
		if sessionStore != nil {
			rec, err := sessionStore.CreateSession(newID, "", providerName, model)
			if err == nil {
				newID = rec.ID
			}
		}
		sess = agent.NewSessionWithStore(newID, providerName, model, sessionStore)

		projCtx, _, _ := agent.FindProjectContext("")
		globCtx, _, _ := agent.LoadGlobalContext()
		sysPrompt := agent.BuildSystemPrompt(projCtx, globCtx)
		sess.AddMessage(provider.Message{
			Role:    "system",
			Content: sysPrompt,
		})
	}

	ignoreMatcher := ignore.LoadMatcher("")

	toolRegistry := tools.NewRegistry()
	shellTimeout := time.Duration(cfg.Tools.Shell.TimeoutSeconds) * time.Second
	toolRegistry.Register(&tools.ShellExecTool{Timeout: shellTimeout})
	toolRegistry.Register(&tools.FileReadTool{IgnoreMatcher: ignoreMatcher})
	toolRegistry.Register(&tools.FileWriteTool{
		IgnoreMatcher: ignoreMatcher,
		OnModified:    sess.TrackModifiedFile,
	})
	toolRegistry.Register(&tools.FilePatchTool{
		IgnoreMatcher: ignoreMatcher,
		OnModified:    sess.TrackModifiedFile,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	mcpManager := mcp.NewManager(cfg.MCP.Servers)
	_ = mcpManager.StartAll(ctx, toolRegistry)
	defer mcpManager.CloseAll()

	approval := tools.NewApprovalGateWithPermissions(cfg.Permissions.AutoApprove, cfg.Permissions.Deny)

	if flagTUI {
		return tui.Run(ctx, providerRegistry, sess, toolRegistry, approval)
	}

	loop := agent.NewAgentLoop(providerRegistry, sess, toolRegistry, approval)
	return loop.Run(ctx)
}

func runMCPAdd(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := args[0]
	command := args[1]
	mcpArgs := args[2:]

	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]config.MCPServerConfig)
	}

	cfg.MCP.Servers[name] = config.MCPServerConfig{
		Command: command,
		Args:    mcpArgs,
	}

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Added MCP server %q: %s %s\n", name, command, strings.Join(mcpArgs, " "))
	return nil
}

func runMCPList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(cfg.MCP.Servers) == 0 {
		fmt.Println("No MCP servers configured.")
		fmt.Println("Add one with: gocode mcp add <name> <command> [args...]")
		return nil
	}

	fmt.Println("Configured MCP Servers:")
	fmt.Println(strings.Repeat("─", 60))
	for name, srv := range cfg.MCP.Servers {
		fmt.Printf("• %-16s %s %s\n", name, srv.Command, strings.Join(srv.Args, " "))
	}
	fmt.Println(strings.Repeat("─", 60))
	return nil
}

func runMCPRemove(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := args[0]
	if _, ok := cfg.MCP.Servers[name]; !ok {
		return fmt.Errorf("MCP server %q not found in config", name)
	}

	delete(cfg.MCP.Servers, name)

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Removed MCP server %q\n", name)
	return nil
}

func runLogs(cmd *cobra.Command, args []string) error {
	logPath := config.LogFilePath()
	file, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No log file found at %s\n", logPath)
			return nil
		}
		return fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read log file %s: %w", logPath, err)
	}

	start := 0
	if flagTail > 0 && len(lines) > flagTail {
		start = len(lines) - flagTail
	}
	for i := start; i < len(lines); i++ {
		fmt.Println(lines[i])
	}

	if !flagFollow {
		return nil
	}

	ctx := cmd.Context()
	reader := bufio.NewReader(file)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					time.Sleep(150 * time.Millisecond)
					continue
				}
				return err
			}
			fmt.Print(line)
		}
	}
}

func runDoctor(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	ctx := context.Background()
	fmt.Println("GoCode Doctor — Diagnostic Health Check")
	fmt.Println("──────────────────────────────────────────")

	ollamaProvider, err := provider.NewOllamaProvider(cfg.Provider.Ollama.Host)
	if err != nil {
		fmt.Printf("✗ %-14s failed initialization: %v\n", "ollama", err)
	} else {
		models, err := ollamaProvider.Models(ctx)
		if err != nil {
			fmt.Printf("✗ %-14s unreachable at %s — is Ollama running?\n", "ollama", cfg.Provider.Ollama.Host)
		} else {
			fmt.Printf("✓ %-14s reachable at %s (%d models available)\n", "ollama", cfg.Provider.Ollama.Host, len(models))
		}
	}

	for _, name := range gatewayProviderNames {
		gCfg := gatewayConfigFor(cfg.Provider, name)
		gw := provider.NewGatewayProxyProvider(name, gCfg)

		hasKey := gCfg.APIKey != ""
		if !hasKey && gCfg.APIKeyEnv != "" {
			hasKey = os.Getenv(gCfg.APIKeyEnv) != ""
		}

		if !hasKey && gCfg.BaseURL != "" && gCfg.APIKeyEnv != "" {
			fmt.Printf("⚠ %-14s no API key (set %s env var or api_key in config)\n", name, gCfg.APIKeyEnv)
			continue
		}

		models, err := gw.Models(ctx)
		if err != nil {
			fmt.Printf("✗ %-14s unreachable at %s\n", name, gCfg.BaseURL)
		} else {
			fmt.Printf("✓ %-14s reachable at %s (default: %s, %d models)\n", name, gCfg.BaseURL, gCfg.DefaultModel, len(models))
		}
	}

	anthropicCfg := cfg.Provider.Anthropic
	hasAnthropicKey := anthropicCfg.APIKey != ""
	if !hasAnthropicKey && anthropicCfg.APIKeyEnv != "" {
		hasAnthropicKey = os.Getenv(anthropicCfg.APIKeyEnv) != ""
	}

	if !hasAnthropicKey {
		fmt.Printf("⚠ %-14s no API key (set %s env var or api_key in config)\n", "anthropic", anthropicCfg.APIKeyEnv)
	} else {
		ap := provider.NewAnthropicProvider(anthropicCfg)
		models, _ := ap.Models(ctx)
		fmt.Printf("✓ %-14s configured (default: %s, %d models known)\n", "anthropic", anthropicCfg.DefaultModel, len(models))
	}

	fmt.Println("──────────────────────────────────────────")
	return nil
}
