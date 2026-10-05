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
	"sort"
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
	// Build-injected version information.
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

var (
	flagProvider  string
	flagModel     string
	flagConfig    string
	flagTUI       bool
	flagVerbose   bool
	flagNew       bool
	flagSessionID string
	flagWorkdir   string
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

	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, buildDate)

	rootCmd.Flags().StringVar(&flagProvider, "provider", "", "LLM provider to use")
	rootCmd.Flags().StringVar(&flagModel, "model", "", "Model to use")
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "Path to config file")
	rootCmd.Flags().StringVar(&flagWorkdir, "workdir", "", "Workspace root directory (defaults to current directory)")
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
		// Server commands take their own flags, so disable parsing; `--` still ends GoCode's flags.
		DisableFlagParsing: true,
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
	rootCmd.AddCommand(newProviderCommand())
	rootCmd.AddCommand(newAuthCommand())

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

// gatewayProviderNames lists built-in OpenAI-compatible providers served by GatewayProxyProvider.
var gatewayProviderNames = []string{
	"omniroute", "openai", "gemini", "groq", "openrouter", "qwen", "kimi",
	"hermes", "xai", "mistral", "minimax", "deepseek", "together",
	"fireworks", "cerebras", "zhipu", "nvidia",
}

// gatewayConfigFor resolves a provider name; an explicit custom entry wins over the built-in.
func gatewayConfigFor(cfg config.ProviderConfig, name string) config.GatewayConfig {
	if custom, ok := cfg.Custom[name]; ok {
		return custom
	}
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
	case "hermes":
		return cfg.Hermes
	case "xai":
		return cfg.XAI
	case "mistral":
		return cfg.Mistral
	case "minimax":
		return cfg.MiniMax
	case "deepseek":
		return cfg.DeepSeek
	case "together":
		return cfg.Together
	case "fireworks":
		return cfg.Fireworks
	case "cerebras":
		return cfg.Cerebras
	case "zhipu":
		return cfg.Zhipu
	case "nvidia":
		return cfg.Nvidia
	case "anthropic":
		return cfg.Anthropic
	default:
		return cfg.Custom[name]
	}
}

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
	providerRegistry.Register(provider.NewCopilotProvider(cfg.Provider.Copilot))
	if err := registerCustomProviders(providerRegistry, cfg.Provider.Custom); err != nil {
		return fmt.Errorf("failed to configure custom providers: %w", err)
	}

	// With persist=false the store is never opened, so nothing is read or written.
	sessionStore, err := openSessionStore(cfg.Session.Persist, filepath.Join(config.SessionDir(), "sessions.db"))
	if err != nil {
		slog.Warn("failed to open session store", "error", err)
		fmt.Fprintf(os.Stderr, "warning: session persistence disabled: %v\n", err)
	}
	if sessionStore != nil {
		defer func() {
			if cerr := sessionStore.Close(); cerr != nil {
				slog.Warn("failed to close session store", "error", cerr)
			}
		}()
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
				ollamaCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if models, err := ollamaProvider.Models(ollamaCtx); err == nil && len(models) > 0 {
					model = models[0]
				} else {
					model = "codellama"
				}
			}
		case "anthropic":
			model = cfg.Provider.Anthropic.DefaultModel
		case "copilot":
			model = cfg.Provider.Copilot.DefaultModel
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
		workspaceRoot := flagWorkdir
		if workspaceRoot == "" {
			workspaceRoot, _ = os.Getwd()
		}
		workspaceRoot, _ = filepath.Abs(workspaceRoot)

		sess = agent.NewSessionWithStore(newID, providerName, model, sessionStore)

		opts := agent.DefaultPromptOptions(workspaceRoot, providerName, model)
		sysPrompt := agent.BuildSystemPromptWithOptions(opts)
		sess.AddMessage(provider.Message{
			Role:    "system",
			Content: sysPrompt,
		})
	}

	workspaceRoot := flagWorkdir
	if workspaceRoot == "" {
		workspaceRoot, _ = os.Getwd()
	}
	workspaceRoot, _ = filepath.Abs(workspaceRoot)

	ignoreMatcher := ignore.LoadMatcher(workspaceRoot)
	sensitiveMatcher := ignore.NewSensitiveMatcher(ignoreMatcher, cfg.Permissions.SensitivePatterns)

	toolRegistry := tools.NewRegistry()
	shellTimeout := time.Duration(cfg.Tools.Shell.TimeoutSeconds) * time.Second
	toolRegistry.Register(&tools.ShellExecTool{
		Timeout:        shellTimeout,
		WorkspaceRoot:  workspaceRoot,
		MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
		Guard: &tools.ShellGuard{
			SensitiveMatcher:   sensitiveMatcher,
			RedactSecretOutput: cfg.Tools.Shell.RedactSecrets,
		},
	})
	toolRegistry.Register(&tools.FileReadTool{
		SensitiveMatcher: sensitiveMatcher,
		WorkspaceRoot:    workspaceRoot,
	})
	toolRegistry.Register(&tools.FileWriteTool{
		SensitiveMatcher: sensitiveMatcher,
		WorkspaceRoot:    workspaceRoot,
		OnModified:       sess.TrackModifiedFile,
	})
	toolRegistry.Register(&tools.FilePatchTool{
		SensitiveMatcher: sensitiveMatcher,
		WorkspaceRoot:    workspaceRoot,
		OnModified:       sess.TrackModifiedFile,
	})
	toolRegistry.Register(&tools.CodeSearchTool{
		SensitiveMatcher: sensitiveMatcher,
		WorkspaceRoot:    workspaceRoot,
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
	if err := mcpManager.StartAll(ctx, toolRegistry); err != nil {
		fmt.Fprintf(os.Stderr, "warning: MCP server startup failed: %v\n", err)
		slog.Warn("mcp startup", "error", err)
	}
	defer mcpManager.CloseAll()

	approval := tools.NewApprovalGateWithPermissions(cfg.Permissions.AutoApprove, cfg.Permissions.Deny)

	// One guard config for both UI paths so they agree on when a turn may stop.
	guardCfg := agent.LoopGuardConfig{
		MaxIterations:    cfg.Tools.MaxToolIterations,
		MaxRepeatedCalls: cfg.Tools.MaxRepeatedToolCalls,
	}

	if flagTUI {
		return tui.Run(ctx, providerRegistry, sess, toolRegistry, approval, version, workspaceRoot, guardCfg, cfg.Tools.MaxContextTokens)
	}

	loop := agent.NewAgentLoop(providerRegistry, sess, toolRegistry, approval)
	loop.WorkspaceRoot = workspaceRoot
	loop.GuardConfig = guardCfg
	loop.ContextManager = agent.NewContextManager(cfg.Tools.MaxContextTokens)
	return loop.Run(ctx)
}

// openSessionStore opens the store when persistence is enabled; split out for testability.
func openSessionStore(persist bool, dbPath string) (*session.SessionStore, error) {
	if !persist {
		slog.Info("session persistence disabled by config")
		return nil, nil
	}
	store, err := session.NewStore(dbPath)
	if err != nil {
		slog.Warn("failed to open session store", "path", dbPath, "error", err)
		return nil, err
	}
	return store, nil
}

func runMCPAdd(cmd *cobra.Command, args []string) error {
	// Flag parsing is off, so extract GoCode's flags by hand.
	rest, configPath := splitMCPAddArgs(args)
	if configPath != "" {
		flagConfig = configPath
	}
	args = rest

	cfg, err := loadCLIConfig()
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

	if err := saveCLIConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Added MCP server %q: %s %s\n", name, command, strings.Join(mcpArgs, " "))
	return nil
}

// splitMCPAddArgs separates GoCode's flags (before the server name) from the server command; `--` ends GoCode's section.
func splitMCPAddArgs(args []string) (serverArgs []string, configPath string) {
	// Split off the post-`--` section first; it can appear anywhere.
	head := args
	var tail []string
	if i := indexOfString(args, "--"); i >= 0 {
		head, tail = args[:i], args[i+1:]
	}

	serverStart := -1
	for i := 0; i < len(head); i++ {
		arg := head[i]
		switch {
		case arg == "--config" || arg == "-config":
			if i+1 < len(head) {
				configPath = head[i+1]
				i++
				continue
			}
		case strings.HasPrefix(arg, "--config="):
			configPath = strings.TrimPrefix(arg, "--config=")
		case arg == "--verbose" || arg == "-verbose" || arg == "-v":
			flagVerbose = true
		default:
			// First positional starts the server command, so --config here belongs to the server.
			serverStart = i
		}
		if serverStart >= 0 {
			break
		}
	}

	if serverStart < 0 {
		serverStart = len(head)
	}

	// A --config after `--` is ours since the separator ended the server command.
	tailArgs, tailConfig := splitTailConfig(tail)
	if tailConfig != "" {
		configPath = tailConfig
	}

	serverArgs = append(append([]string{}, head[serverStart:]...), tailArgs...)
	if len(serverArgs) == 0 {
		serverArgs = nil
	}
	return serverArgs, configPath
}

func splitTailConfig(tail []string) (rest []string, configPath string) {
	for i := 0; i < len(tail); i++ {
		a := tail[i]
		switch {
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
			rest = append(rest, removeAt(tail, i)...)
			return rest, configPath
		case (a == "--config" || a == "-config") && i+1 < len(tail):
			configPath = tail[i+1]
			rest = append(rest, removeSlice(tail, i, i+2)...)
			return rest, configPath
		}
	}
	return tail, ""
}

func indexOfString(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}

func removeAt(args []string, i int) []string {
	return append(args[:i:i], args[i+1:]...)
}

func removeSlice(args []string, from, to int) []string {
	out := make([]string, 0, len(args)-(to-from))
	out = append(out, args[:from]...)
	return append(out, args[to:]...)
}

func runMCPList(cmd *cobra.Command, args []string) error {
	cfg, err := loadCLIConfig()
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
	cfg, err := loadCLIConfig()
	if err != nil {
		return err
	}

	name := args[0]
	if _, ok := cfg.MCP.Servers[name]; !ok {
		return fmt.Errorf("MCP server %q not found in config", name)
	}

	delete(cfg.MCP.Servers, name)

	if err := saveCLIConfig(cfg); err != nil {
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
	cfg, err := loadCLIConfig()
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

	providerNames := append([]string(nil), gatewayProviderNames...)
	customNames := make([]string, 0, len(cfg.Provider.Custom))
	for name := range cfg.Provider.Custom {
		customNames = append(customNames, name)
	}
	sort.Strings(customNames)
	providerNames = append(providerNames, customNames...)
	for _, name := range providerNames {
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

	// Ask the provider for token state so `doctor` agrees with actual auth (file tokens included).
	cp := provider.NewCopilotProvider(cfg.Provider.Copilot)
	fmt.Println(copilotDoctorLine(cp, ctx, cfg.Provider.Copilot.DefaultModel))

	fmt.Println("──────────────────────────────────────────")
	return nil
}

// copilotDoctorLine renders the `doctor` status line; it asks the provider so reported state matches actual auth.
func copilotDoctorLine(cp *provider.CopilotProvider, ctx context.Context, defaultModel string) string {
	if !cp.HasToken() {
		return fmt.Sprintf("⚠ %-14s no GitHub OAuth token (run `gocode auth copilot`, or set $%s)", "copilot", cp.TokenEnvName())
	}
	models, err := cp.Models(ctx)
	if err != nil {
		return fmt.Sprintf("✗ %-14s token exchange failed: %v", "copilot", err)
	}
	return fmt.Sprintf("✓ %-14s reachable (default: %s, %d models)", "copilot", defaultModel, len(models))
}
