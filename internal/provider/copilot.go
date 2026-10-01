package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mevarx/GoCode/internal/config"
)

const (
	// copilotTokenURL exchanges a long-lived GitHub OAuth token for the
	// short-lived Copilot JWT plus the API base URL to use with it.
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

	// copilotFallbackAPI is used when the exchange does not return a base
	// URL. Both the exchange and the allowlist below are reverse-engineered
	// from the VS Code Copilot Chat extension: this endpoint is undocumented
	// by GitHub and can change without notice.
	copilotFallbackAPI = "https://api.githubcopilot.com"

	// copilotTokenRefreshMargin refreshes the JWT before it actually expires,
	// so a long turn does not start a request with a token that dies mid-stream.
	copilotTokenRefreshMargin = 60 * time.Second

	// maxTokenResponseBytes caps the exchange response. The real payload is a
	// few hundred bytes; this bounds memory if the endpoint misbehaves.
	maxTokenResponseBytes = 64 << 10

	// copilotClientID is the public client id GitHub's own Copilot Chat
	// clients present during the token exchange. It is not a secret and
	// grants no access on its own; it only identifies the client to GitHub.
	copilotClientID = "Iv1.b507a08c87ecfe98"
)

// copilotKnownModels is what Copilot exposes when the token exchange succeeds
// but the dynamic endpoint does not serve a usable /models response.
var copilotKnownModels = []string{
	"gpt-4.1",
	"gpt-4o",
	"gpt-5",
	"claude-sonnet-4.5",
	"claude-sonnet-4",
	"gemini-2.5-pro",
	"o3-mini",
	"gpt-5-mini",
	"gpt-5-codex",
}

// CopilotProvider talks to GitHub Copilot's OpenAI-compatible endpoint.
//
// Copilot is not a plain bearer-token API. It uses two layers: a long-lived
// GitHub OAuth token (ghu_...) is exchanged for a short-lived Copilot JWT, and
// that exchange also returns the API base URL to address. The JWT is cached
// until shortly before it expires.
type CopilotProvider struct {
	cfg    config.CopilotConfig
	client *http.Client

	// tokenURL is the OAuth exchange endpoint. It is a field rather than a
	// bare constant so tests can point it at a local server.
	tokenURL string

	// allowAPIURL validates the base URL returned by the exchange. It is a
	// field so tests can permit a local server; production always uses
	// validCopilotAPIURL.
	allowAPIURL func(*url.URL) bool

	mu        sync.Mutex
	cachedJWT string
	cachedAPI string
	expiresAt time.Time
	// cachedFor records which OAuth token produced the cached JWT, so a
	// rotated token is detected instead of silently reusing a stale JWT.
	cachedFor string
}

// NewCopilotProvider builds a Copilot provider from config.
func NewCopilotProvider(cfg config.CopilotConfig) *CopilotProvider {
	baseTransport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &CopilotProvider{
		cfg:         cfg,
		tokenURL:    copilotTokenURL,
		allowAPIURL: validCopilotAPIURL,
		client: &http.Client{
			Transport: NewRetryTransport(baseTransport),
		},
	}
}

func (p *CopilotProvider) Name() string {
	return "copilot"
}

// oauthToken resolves the GitHub OAuth token. It prefers the environment
// variable and falls back to the file written by `gocode auth copilot`, so a
// user who logged in once does not have to export a variable in every shell.
func (p *CopilotProvider) oauthToken() string {
	if p.cfg.OAuthTokenEnv != "" {
		if token := strings.TrimSpace(os.Getenv(p.cfg.OAuthTokenEnv)); token != "" {
			return token
		}
	}
	if token, err := os.ReadFile(config.CopilotTokenPath()); err == nil {
		return strings.TrimSpace(string(token))
	}
	return ""
}

// applyIntegrationHeaders sets the headers Copilot requires to treat a request
// as coming from a real editor client. Copilot rejects requests without them.
func (p *CopilotProvider) applyIntegrationHeaders(req *http.Request) {
	editorVersion := p.cfg.EditorVersion
	if editorVersion == "" {
		editorVersion = "vscode/1.111.0"
	}
	pluginVersion := p.cfg.EditorPluginVersion
	if pluginVersion == "" {
		pluginVersion = "copilot-chat/0.40.0"
	}
	req.Header.Set("Editor-Version", editorVersion)
	req.Header.Set("Editor-Plugin-Version", pluginVersion)
	req.Header.Set("User-Agent", "GitHubCopilotChat/"+strings.TrimPrefix(pluginVersion, "copilot-chat/"))
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
}

// validCopilotAPIURL reports whether u is an endpoint we are willing to send
// an authenticated Copilot JWT to.
//
// The base URL arrives from the network in the token exchange response, so it
// is untrusted input. Without this check a compromised or spoofed exchange
// response could redirect the user's GitHub token to an attacker-controlled
// host. Only HTTPS and only GitHub-owned Copilot hosts are accepted.
func validCopilotAPIURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	if u.User != nil {
		// Credentials embedded in the URL are a phishing/redirect trick and
		// have no legitimate use here.
		return false
	}
	if u.RawQuery != "" || u.Fragment != "" {
		// The base URL is concatenated with "/chat/completions"; a query or
		// fragment would swallow that path.
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return false
	}
	if strings.Contains(u.Host, ":") {
		// A port makes the host something other than a bare GitHub domain.
		return false
	}
	// The leading dot in the suffix is what stops "api.githubcopilot.com.evil.com".
	return host == "api.githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com") ||
		host == "api.github.com" || strings.HasSuffix(host, ".github.com")
}

// resolveCopilotAPI validates the base URL from the exchange response,
// falling back when it is absent. An untrusted or unencrypted URL is a hard
// error rather than a silent downgrade, so a credential is never sent
// somewhere the allowlist rejects.
func resolveCopilotAPI(raw string, allow func(*url.URL) bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return copilotFallbackAPI, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return copilotFallbackAPI, fmt.Errorf("copilot token exchange returned an unparseable API base URL: %w", err)
	}
	if !allow(parsed) {
		return copilotFallbackAPI, fmt.Errorf("copilot token exchange returned an untrusted API base URL (%q); refusing to send credentials there", raw)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// exchangeToken trades the GitHub OAuth token for a Copilot JWT and base URL.
func (p *CopilotProvider) exchangeToken(ctx context.Context, oauthToken string) (string, string, time.Time, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":              copilotClientID,
		"editor":                 "vscode",
		"editor_plugin":          "copilot-chat",
		"editor_version":         strings.TrimPrefix(p.editorVersion(), "vscode/"),
		"user_agent":             "GitHubCopilotChat",
		"copilot_integration_id": "vscode-chat",
	})
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to build copilot token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create copilot token request: %w", err)
	}
	// The exchange is a GET in the VS Code client; the body is sent anyway so
	// the request matches observed traffic.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+oauthToken)
	req.Header.Set("Content-Type", "application/json")
	p.applyIntegrationHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("copilot token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to read copilot token response: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return "", "", time.Time{}, fmt.Errorf("copilot rejected the GitHub token (status %d): the token may be expired, or the account has no active Copilot subscription", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", time.Time{}, fmt.Errorf("copilot token exchange returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed copilotTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to decode copilot token response: %w", err)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return "", "", time.Time{}, fmt.Errorf("copilot token exchange returned an empty token; the account likely has no active Copilot subscription")
	}

	apiBase, apiErr := resolveCopilotAPI(parsed.Endpoints.API, p.allowAPIURL)
	if apiErr != nil && parsed.Endpoints.API != "" {
		// An untrusted base URL is a hard error, not a silent downgrade.
		return "", "", time.Time{}, apiErr
	}

	var expiry time.Time
	if parsed.ExpiresAt > 0 {
		expiry = time.Unix(parsed.ExpiresAt, 0)
	} else {
		// Without an explicit expiry, assume the documented ~30 minute
		// lifetime rather than caching indefinitely.
		expiry = time.Now().Add(30 * time.Minute)
	}

	return parsed.Token, apiBase, expiry, nil
}

func (p *CopilotProvider) editorVersion() string {
	if p.cfg.EditorVersion != "" {
		return p.cfg.EditorVersion
	}
	return "vscode/1.111.0"
}

// token returns a usable Copilot JWT and API base URL, exchanging only when
// the cache is missing, stale, expired, or was minted for a different OAuth
// token.
func (p *CopilotProvider) token(ctx context.Context) (string, string, error) {
	oauthToken := p.oauthToken()
	if oauthToken == "" {
		envName := p.cfg.OAuthTokenEnv
		if envName == "" {
			envName = "GITHUB_COPILOT_TOKEN"
		}
		return "", "", fmt.Errorf("no GitHub OAuth token for copilot: set $%s or run `gocode auth copilot`", envName)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cachedJWT != "" && p.cachedFor == oauthToken && time.Now().Before(p.expiresAt.Add(-copilotTokenRefreshMargin)) {
		return p.cachedJWT, p.cachedAPI, nil
	}

	jwt, apiBase, expiry, err := p.exchangeToken(ctx, oauthToken)
	if err != nil {
		return "", "", err
	}

	p.cachedJWT = jwt
	p.cachedAPI = apiBase
	p.expiresAt = expiry
	p.cachedFor = oauthToken
	return jwt, apiBase, nil
}

func (p *CopilotProvider) Models(ctx context.Context) ([]string, error) {
	jwt, apiBase, err := p.token(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create copilot models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	p.applyIntegrationHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		// A model list is a convenience; fall back to the known set rather
		// than failing the whole provider.
		return append([]string(nil), copilotKnownModels...), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return append([]string(nil), copilotKnownModels...), nil
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenResponseBytes)).Decode(&result); err != nil {
		return append([]string(nil), copilotKnownModels...), nil
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	if len(models) == 0 {
		return append([]string(nil), copilotKnownModels...), nil
	}
	sort.Strings(models)
	return models, nil
}

func (p *CopilotProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
	jwt, apiBase, err := p.token(ctx)
	if err != nil {
		return nil, err
	}

	messages := buildOpenAIMessages(history)

	payload := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	if len(tools) > 0 {
		payload["tools"] = buildOpenAITools(tools)
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal copilot request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create copilot stream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+jwt)
	p.applyIntegrationHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot unreachable at %s: %w", apiBase, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("copilot returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer resp.Body.Close()
		defer close(ch)
		streamOpenAISSE(resp.Body, ch, "copilot")
	}()

	return ch, nil
}

// buildOpenAIMessages converts the provider history into OpenAI wire messages.
// Shared with the gateway proxy so both send byte-identical history.
func buildOpenAIMessages(history []Message) []openAIMessage {
	messages := make([]openAIMessage, 0, len(history))
	for _, msg := range history {
		oMsg := openAIMessage{
			Role:             msg.Role,
			Content:          msg.Content,
			ToolCallID:       msg.ToolCallID,
			ReasoningContent: msg.ReasoningContent,
		}
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				oMsg.ToolCalls = append(oMsg.ToolCalls, openAIToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: openAIToolCallFunction{
						Name:      tc.Name,
						Arguments: string(tc.Args),
					},
				})
			}
		}
		messages = append(messages, oMsg)
	}
	return messages
}

// buildOpenAITools converts tool specs into OpenAI wire tools.
func buildOpenAITools(tools []ToolSpec) []openAIToolSpec {
	toolSpecs := make([]openAIToolSpec, 0, len(tools))
	for _, ts := range tools {
		toolSpecs = append(toolSpecs, openAIToolSpec{
			Type: "function",
			Function: openAIToolFunction{
				Name:        ts.Name,
				Description: ts.Description,
				Parameters:  ts.Parameters,
			},
		})
	}
	return toolSpecs
}

// streamOpenAISSE parses an OpenAI-format SSE body into StreamChunks.
// Shared by the gateway proxy and Copilot so streaming behaviour — tool-call
// assembly, reasoning passthrough, [DONE] handling — cannot drift between them.
func streamOpenAISSE(body io.Reader, ch chan<- StreamChunk, source string) {
	scanner := bufio.NewScanner(body)
	// Tool-call argument fragments and reasoning deltas can be large; the
	// default 64 KiB line cap truncates long tool arguments into a parse error.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	type pendingToolCall struct {
		id   string
		name string
		args strings.Builder
	}
	pendingTools := make(map[int]*pendingToolCall)

	emitToolCalls := func(flush bool) []ToolCall {
		if len(pendingTools) == 0 {
			return nil
		}
		indices := make([]int, 0, len(pendingTools))
		for idx := range pendingTools {
			indices = append(indices, idx)
		}
		sort.Ints(indices)

		var calls []ToolCall
		for _, idx := range indices {
			pt := pendingTools[idx]
			argsStr := pt.args.String()
			if strings.TrimSpace(argsStr) == "" {
				argsStr = "{}"
			}
			calls = append(calls, ToolCall{
				ID:   pt.id,
				Name: pt.name,
				Args: json.RawMessage(argsStr),
			})
		}
		if flush {
			pendingTools = make(map[int]*pendingToolCall)
		}
		return calls
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			// Copilot emits named SSE events (for example
			// `event: hermes.tool.progress`); the payload is on the
			// following data: line, so non-data lines are skipped.
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			ch <- StreamChunk{ToolCalls: emitToolCalls(true), Done: true}
			return
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				ch <- StreamChunk{Delta: choice.Delta.Content}
			}
			if choice.Delta.ReasoningContent != "" {
				ch <- StreamChunk{Reasoning: choice.Delta.ReasoningContent}
			}

			for _, tc := range choice.Delta.ToolCalls {
				idx := tc.Index
				pt, exists := pendingTools[idx]
				if !exists {
					pt = &pendingToolCall{}
					pendingTools[idx] = pt
				}
				if tc.ID != "" {
					pt.id = tc.ID
				}
				if tc.Function.Name != "" {
					pt.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					pt.args.WriteString(tc.Function.Arguments)
				}
				if pt.id == "" && pt.name != "" {
					pt.id = fmt.Sprintf("call_%s_%d", pt.name, idx)
				}
			}

			if choice.FinishReason == "tool_calls" || choice.FinishReason == "stop" {
				if calls := emitToolCalls(true); len(calls) > 0 {
					ch <- StreamChunk{ToolCalls: calls}
				}
			}
		}
	}

	if calls := emitToolCalls(true); len(calls) > 0 {
		ch <- StreamChunk{ToolCalls: calls}
	}
	if err := scanner.Err(); err != nil {
		ch <- StreamChunk{Err: fmt.Errorf("error reading stream from %s: %w", source, err), Done: true}
	}
}
