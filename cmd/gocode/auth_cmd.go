package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mevarx/GoCode/internal/config"
)

// GitHub OAuth device-flow endpoints; device flow is the only way to obtain a Copilot-usable token.
const (
	githubDeviceCodeURL  = "https://github.com/login/device/code"
	githubAccessTokenURL = "https://github.com/login/oauth/access_token"
	githubOAuthScope     = "read:user"
)

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
}

// Errors arrive as HTTP 200 with an error field.
type accessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with providers that require an OAuth grant",
	}

	var clientID string
	var timeout time.Duration
	copilotCmd := &cobra.Command{
		Use:   "copilot",
		Short: "Sign in to GitHub Copilot using the OAuth device flow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCopilotLogin(cmd, clientID, timeout)
		},
	}
	copilotCmd.Flags().StringVar(&clientID, "client-id", "", "GitHub OAuth App client id to authorize (required)")
	copilotCmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait for the user to authorize in the browser")
	_ = copilotCmd.MarkFlagRequired("client-id")

	cmd.AddCommand(copilotCmd)
	return cmd
}

// runCopilotLogin walks the device flow and stores the token; a child process cannot export env vars, so it persists to a file.
// The token is written with owner-only permissions.
func runCopilotLogin(cmd *cobra.Command, clientID string, timeout time.Duration) error {
	if clientID == "" {
		return fmt.Errorf("--client-id is required: register an OAuth App at https://github.com/settings/developers and copy its client id")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	httpClient := &http.Client{Timeout: 30 * time.Second}

	out := cmd.OutOrStdout()

	device, err := requestDeviceCode(ctx, httpClient, clientID)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Open %s in your browser and enter code: %s\n", device.VerificationURI, device.UserCode)
	fmt.Fprintln(out, "Waiting for authorization...")

	token, err := pollForToken(ctx, httpClient, clientID, device)
	if err != nil {
		return err
	}

	path, err := saveCopilotToken(token)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "✓ GitHub Copilot authorized. Token saved to %s\n", path)
	// The provider reads this file automatically; exporting a live credential into every shell is worse than one 0600 file.
	fmt.Fprintf(out, "  It is picked up automatically. Run: gocode --provider copilot\n")
	fmt.Fprintf(out, "  (%s takes precedence if you prefer to set it.)\n", "GITHUB_COPILOT_TOKEN")
	return nil
}

func requestDeviceCode(ctx context.Context, client *http.Client, clientID string) (*deviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", githubOAuthScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to build device-code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach GitHub device-code endpoint: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("failed to read device-code response: %w", err)
	}

	var parsed deviceCodeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("failed to decode device-code response: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("GitHub rejected the device-code request: %s", parsed.Error)
	}
	if parsed.UserCode == "" || parsed.DeviceCode == "" {
		return nil, fmt.Errorf("GitHub returned an incomplete device code (user_code or device_code missing); check the OAuth App's client id")
	}
	if parsed.VerificationURI == "" {
		parsed.VerificationURI = "https://github.com/login/device"
	}
	return &parsed, nil
}

// pollForToken exchanges the device code, honouring GitHub's polling interval.
func pollForToken(ctx context.Context, client *http.Client, clientID string, device *deviceCodeResponse) (string, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for GitHub authorization")
		case <-ticker.C:
		}

		form := url.Values{}
		form.Set("client_id", clientID)
		form.Set("device_code", device.DeviceCode)
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", fmt.Errorf("failed to build access-token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to reach GitHub access-token endpoint: %w", err)
		}

		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("failed to read access-token response: %w", err)
		}

		var parsed accessTokenResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", fmt.Errorf("failed to decode access-token response: %w", err)
		}

		switch parsed.Error {
		case "":
			if parsed.AccessToken == "" {
				return "", fmt.Errorf("GitHub returned no access token and no error")
			}
			return parsed.AccessToken, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			ticker.Reset(interval)
			continue
		case "expired_token":
			return "", fmt.Errorf("the device code expired before it was authorized; run the command again")
		case "access_denied":
			return "", fmt.Errorf("authorization was denied")
		default:
			desc := parsed.ErrorDescription
			if desc == "" {
				desc = parsed.Error
			}
			return "", fmt.Errorf("GitHub rejected the authorization: %s", desc)
		}
	}
}

// saveCopilotToken writes the live credential with owner-only permissions.
func saveCopilotToken(token string) (string, error) {
	if err := os.MkdirAll(config.ConfigDir(), 0o700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}
	path := config.CopilotTokenPath()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(token)), 0o600); err != nil {
		return "", fmt.Errorf("failed to write Copilot token: %w", err)
	}
	return path, nil
}
