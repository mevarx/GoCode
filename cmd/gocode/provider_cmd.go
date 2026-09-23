package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/provider"
)

var (
	providerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	apiKeyEnvPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func newProviderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage API-compatible model providers",
	}

	var baseURL, apiKeyEnv, model string
	addCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a custom OpenAI-compatible API endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCLIConfig()
			if err != nil {
				return err
			}
			name, endpoint, err := addCustomProvider(&cfg, args[0], config.GatewayConfig{
				BaseURL:      baseURL,
				APIKeyEnv:    apiKeyEnv,
				DefaultModel: model,
			})
			if err != nil {
				return err
			}
			if err := saveCLIConfig(cfg); err != nil {
				return fmt.Errorf("failed to save provider configuration: %w", err)
			}
			keySource := "no API key"
			if endpoint.APIKeyEnv != "" {
				keySource = "$" + endpoint.APIKeyEnv
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Configured provider %q (%s, model %s, key %s)\n", name, endpoint.BaseURL, endpoint.DefaultModel, keySource)
			return nil
		},
	}
	addCmd.Flags().StringVar(&baseURL, "base-url", "", "Base URL for an OpenAI Chat Completions-compatible API")
	addCmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "", "Environment variable containing the API key (never stores the key itself)")
	addCmd.Flags().StringVar(&model, "model", "", "Default model identifier for this endpoint")
	_ = addCmd.MarkFlagRequired("base-url")
	_ = addCmd.MarkFlagRequired("model")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List configured built-in and custom providers",
		Args:  cobra.NoArgs,
		RunE:  runProviderList,
	}
	removeCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a custom provider endpoint",
		Args:  cobra.ExactArgs(1),
		RunE:  runProviderRemove,
	}

	cmd.AddCommand(addCmd, listCmd, removeCmd)
	return cmd
}

func loadCLIConfig() (config.Config, error) {
	if flagConfig != "" {
		cfg, err := config.LoadFromPath(flagConfig)
		if errors.Is(err, os.ErrNotExist) {
			return config.DefaultConfig(), nil
		}
		return cfg, err
	}
	return config.Load()
}

func saveCLIConfig(cfg config.Config) error {
	if flagConfig != "" {
		return config.SaveToPath(cfg, flagConfig)
	}
	return config.Save(cfg)
}

func addCustomProvider(cfg *config.Config, name string, endpoint config.GatewayConfig) (string, config.GatewayConfig, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !providerNamePattern.MatchString(name) {
		return "", config.GatewayConfig{}, fmt.Errorf("invalid provider name %q: use 1-32 lowercase letters, digits, hyphens, or underscores; it must start with a letter", name)
	}
	if isBuiltInProvider(name) {
		return "", config.GatewayConfig{}, fmt.Errorf("%q is a built-in provider name", name)
	}
	baseURL, err := validateProviderBaseURL(endpoint.BaseURL)
	if err != nil {
		return "", config.GatewayConfig{}, err
	}
	endpoint.BaseURL = baseURL
	endpoint.DefaultModel = strings.TrimSpace(endpoint.DefaultModel)
	if endpoint.DefaultModel == "" {
		return "", config.GatewayConfig{}, fmt.Errorf("default model cannot be empty")
	}
	endpoint.APIKeyEnv = strings.TrimSpace(endpoint.APIKeyEnv)
	if endpoint.APIKeyEnv != "" && !apiKeyEnvPattern.MatchString(endpoint.APIKeyEnv) {
		return "", config.GatewayConfig{}, fmt.Errorf("invalid API key environment variable name %q", endpoint.APIKeyEnv)
	}
	if cfg.Provider.Custom == nil {
		cfg.Provider.Custom = make(map[string]config.GatewayConfig)
	}
	cfg.Provider.Custom[name] = endpoint
	return name, endpoint, nil
}

func validateProviderBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("base URL must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("base URL must not contain credentials, a query, or a fragment")
	}
	if parsed.Scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && !strings.HasSuffix(host, ".localhost") && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("base URL must use HTTPS unless it targets localhost")
		}
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isBuiltInProvider(name string) bool {
	switch name {
	case "ollama", "anthropic":
		return true
	}
	for _, builtIn := range gatewayProviderNames {
		if name == builtIn {
			return true
		}
	}
	return false
}

func registerCustomProviders(registry *provider.Registry, configured map[string]config.GatewayConfig) error {
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := make(map[string]struct{}, len(names))
	for _, rawName := range names {
		name, endpoint, err := addCustomProvider(&config.Config{}, rawName, configured[rawName])
		if err != nil {
			return fmt.Errorf("custom provider %q: %w", rawName, err)
		}
		if name != rawName {
			return fmt.Errorf("custom provider name %q must be lowercase", rawName)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate custom provider name %q", name)
		}
		seen[name] = struct{}{}
		registry.Register(provider.NewGatewayProxyProvider(name, endpoint))
	}
	return nil
}

func runProviderList(cmd *cobra.Command, args []string) error {
	cfg, err := loadCLIConfig()
	if err != nil {
		return err
	}

	names := append([]string(nil), gatewayProviderNames...)
	names = append(names, "anthropic", "ollama")
	sort.Strings(names)
	fmt.Fprintln(cmd.OutOrStdout(), "Built-in providers:")
	for _, name := range names {
		endpoint := gatewayConfigFor(cfg.Provider, name)
		if name == "ollama" {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s  model=%s\n", name, cfg.Provider.Ollama.Host, cfg.Provider.Ollama.DefaultModel)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s  model=%s  key=%s\n", name, endpoint.BaseURL, endpoint.DefaultModel, apiKeySource(endpoint))
	}

	customNames := make([]string, 0, len(cfg.Provider.Custom))
	for name := range cfg.Provider.Custom {
		customNames = append(customNames, name)
	}
	sort.Strings(customNames)
	fmt.Fprintln(cmd.OutOrStdout(), "Custom providers:")
	if len(customNames) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  none (use `gocode provider add` to configure one)")
	}
	for _, name := range customNames {
		endpoint := cfg.Provider.Custom[name]
		fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s  model=%s  key=%s\n", name, endpoint.BaseURL, endpoint.DefaultModel, apiKeySource(endpoint))
	}
	return nil
}

func apiKeySource(endpoint config.GatewayConfig) string {
	if endpoint.APIKey != "" {
		return "inline (value hidden)"
	}
	if endpoint.APIKeyEnv != "" {
		return "$" + endpoint.APIKeyEnv
	}
	return "none"
}

func runProviderRemove(cmd *cobra.Command, args []string) error {
	cfg, err := loadCLIConfig()
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))
	if _, ok := cfg.Provider.Custom[name]; !ok {
		return fmt.Errorf("custom provider %q not found", name)
	}
	if cfg.Provider.Default == name {
		return fmt.Errorf("%q is the default provider; change provider.default before removing it", name)
	}
	delete(cfg.Provider.Custom, name)
	if err := saveCLIConfig(cfg); err != nil {
		return fmt.Errorf("failed to save provider configuration: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Removed custom provider %q\n", name)
	return nil
}
