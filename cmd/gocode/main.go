package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/gocode-cli/gocode/internal/agent"
	"github.com/gocode-cli/gocode/internal/config"
	"github.com/gocode-cli/gocode/internal/provider"
	"github.com/gocode-cli/gocode/internal/tools"
)

var (
	flagProvider string
	flagModel    string
	flagConfig   string
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

	omniProvider := provider.NewGatewayProxyProvider("omniroute", cfg.Provider.OmniRoute)
	providerRegistry.Register(omniProvider)

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
		case "omniroute":
			model = cfg.Provider.OmniRoute.DefaultModel
			if model == "" {
				model = "auto"
			}
		}
	}

	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(&tools.ShellExecTool{})
	toolRegistry.Register(&tools.FileReadTool{})
	toolRegistry.Register(&tools.FileWriteTool{})
	toolRegistry.Register(&tools.FilePatchTool{})

	approval := tools.NewApprovalGate()

	session := agent.NewSession(model)
	loop := agent.NewAgentLoop(providerRegistry, session, toolRegistry, approval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nInterrupted. Goodbye!")
		cancel()
		os.Exit(0)
	}()

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

	// 1. Check Ollama
	ollamaProvider, err := provider.NewOllamaProvider(cfg.Provider.Ollama.Host)
	if err != nil {
		fmt.Printf("✗ ollama       failed initialization: %v\n", err)
	} else {
		models, err := ollamaProvider.Models(ctx)
		if err != nil {
			fmt.Printf("✗ ollama       unreachable at %s — is Ollama running?\n", cfg.Provider.Ollama.Host)
		} else {
			fmt.Printf("✓ ollama       reachable at %s (%d models available)\n", cfg.Provider.Ollama.Host, len(models))
		}
	}

	// 2. Check OmniRoute
	omniProvider := provider.NewGatewayProxyProvider("omniroute", cfg.Provider.OmniRoute)
	models, err := omniProvider.Models(ctx)
	if err != nil {
		fmt.Printf("✗ omniroute    unreachable at %s — run `npm install -g omniroute && omniroute` first\n", cfg.Provider.OmniRoute.BaseURL)
	} else {
		defaultModel := cfg.Provider.OmniRoute.DefaultModel
		if defaultModel == "" {
			defaultModel = "auto"
		}
		fmt.Printf("✓ omniroute    reachable at %s (default model: %s, %d models reported)\n", cfg.Provider.OmniRoute.BaseURL, defaultModel, len(models))
	}

	fmt.Println("──────────────────────────────────────────")
	return nil
}
