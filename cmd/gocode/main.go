package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
	"github.com/mevarx/GoCode/internal/tui"
)

var (
	flagProvider string
	flagModel    string
	flagConfig   string
	flagTUI      bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "gocode",
		Short: "GoCode — Terminal coding agent",
		Long:  "A Go-native terminal coding agent. Local-first, provider-agnostic, approval-gated.",
		RunE:  runAgent,
	}

	rootCmd.Flags().StringVar(&flagProvider, "provider", "", "LLM provider to use")
	rootCmd.Flags().StringVar(&flagModel, "model", "", "Model to use")
	rootCmd.Flags().StringVar(&flagConfig, "config", "", "Path to config file")
	rootCmd.Flags().BoolVar(&flagTUI, "tui", true, "Enable rich TUI (set --tui=false for plain mode)")

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check health and reachability of configured AI providers",
		RunE:  runDoctor,
	}
	rootCmd.AddCommand(doctorCmd)

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

func runAgent(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := config.EnsureDirs(); err != nil {
		return fmt.Errorf("failed to create data dirs: %w", err)
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

	providerName := cfg.Provider.Default
	if flagProvider != "" {
		providerName = flagProvider
	}
	if err := providerRegistry.Switch(providerName); err != nil {
		return fmt.Errorf("failed to select provider: %w", err)
	}

	var model string
	if flagModel != "" {
		model = flagModel
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

	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(&tools.ShellExecTool{})
	toolRegistry.Register(&tools.FileReadTool{})
	toolRegistry.Register(&tools.FileWriteTool{})
	toolRegistry.Register(&tools.FilePatchTool{})

	approval := tools.NewApprovalGate()

	session := agent.NewSession(model)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		os.Exit(0)
	}()

	if flagTUI {
		return tui.Run(ctx, providerRegistry, session, toolRegistry, approval)
	}

	// plain mode: use --tui=false
	loop := agent.NewAgentLoop(providerRegistry, session, toolRegistry, approval)
	return loop.Run(ctx)
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
