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

	providerName := cfg.Provider.Default
	if flagProvider != "" {
		providerName = flagProvider
	}
	if err := providerRegistry.Switch(providerName); err != nil {
		return fmt.Errorf("failed to select provider: %w", err)
	}

	model := cfg.Provider.Ollama.DefaultModel
	if flagModel != "" {
		model = flagModel
	}
	if model == "" && providerName == "ollama" {
		if models, err := ollamaProvider.Models(context.Background()); err == nil && len(models) > 0 {
			model = models[0]
		} else {
			model = "codellama"
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
