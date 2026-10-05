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
	// copilotTokenURL exchanges the GitHub OAuth token for a short-lived JWT plus API base URL.
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

	// Fallback when exchange returns no base URL; endpoint is undocumented and reverse-engineered, may change.
	copilotFallbackAPI = "https://api.githubcopilot.com"

	// Refresh JWT early so a long turn doesn't start with a token that dies mid-stream.
	copilotTokenRefreshMargin = 60 * time.Second

	// Caps exchange response; real payload is a few hundred bytes.
	maxTokenResponseBytes = 64 << 10

	// Public client id from Copilot Chat clients; not a secret, only identifies the client.
	copilotClientID = "Iv1.b507a08c87ecfe98"
)

// CopilotProvider talks to Copilot's OpenAI-compatible endpoint; exchanges OAuth token for cached short-lived JWT.
type CopilotProvider struct {
	cfg    config.CopilotConfig
	client *http.Client

	// tokenURL is a field (not a constant) so tests can point at a local server.
	tokenURL string

	// allowAPIURL is a field so tests can permit a local server.
	allowAPIURL func(*url.URL) bool

	mu        sync.Mutex
	cachedJWT string
	cachedAPI string
	expiresAt time.Time
	// cachedFor detects rotated OAuth tokens instead of reusing a stale JWT.
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
			// Ceiling so a stalled server can't hold a turn forever.
			Timeout: 10 * time.Minute,
		},
	}
}

func (p *CopilotProvider) Name() string {
	return "copilot"
}

// oauthToken prefers env var, falls back to file from `gocode auth copilot`.
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

// HasToken reports OAuth token availability for `gocode doctor`; checks env and saved file.
func (p *CopilotProvider) HasToken() bool {
	return p.oauthToken() != ""
}

// TokenEnvName returns the environment variable this provider reads first.
func (p *CopilotProvider) TokenEnvName() string {
	if p.cfg.OAuthTokenEnv != "" {
		return p.cfg.OAuthTokenEnv
	}
	return "GITHUB_COPILOT_TOKEN"
}

// applyIntegrationHeaders sets editor headers Copilot requires; requests without them are rejected.
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

// validCopilotAPIURL allows only HTTPS GitHub-owned hosts; base URL is untrusted exchange input.
func validCopilotAPIURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	if u.User != nil {
		// Reject embedded credentials; phishing risk, no legitimate use.
		return false
	}
	if u.RawQuery != "" || u.Fragment != "" {
		// Reject query/fragment; they would swallow the appended "/chat/completions" path.
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return false
	}
	if strings.Contains(u.Host, ":") {
		// Reject ports; host must be a bare GitHub domain.
		return false
	}
	// Leading dot stops "api.githubcopilot.com.evil.com".
	return host == "api.githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com") ||
		host == "api.github.com" || strings.HasSuffix(host, ".github.com")
}

// resolveCopilotAPI validates the exchange base URL, falling back when absent; untrusted URLs are hard errors.
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
	// GET with body to match observed VS Code client traffic.
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

	apiBase, err := resolveCopilotAPI(parsed.Endpoints.API, p.allowAPIURL)
	if err != nil {
		return "", "", time.Time{}, err
	}

	var expiry time.Time
	if parsed.ExpiresAt > 0 {
		expiry = time.Unix(parsed.ExpiresAt, 0)
	} else {
		// Without expiry assume ~30 minute lifetime; don't cache indefinitely.
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

// token returns cached JWT/URL, re-exchanging when missing, expired, or OAuth token rotated.
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

// invalidateToken drops cached JWT so next call re-exchanges; 401/403 must not fail every remaining turn.
func (p *CopilotProvider) invalidateToken() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cachedJWT = ""
	p.cachedAPI = ""
	p.expiresAt = time.Time{}
	p.cachedFor = ""
}

func (p *CopilotProvider) Models(ctx context.Context) ([]string, error) {
	jwt, apiBase, err := p.token(ctx)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create copilot models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	p.applyIntegrationHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot models endpoint unreachable at %s: %w", apiBase, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return nil, fmt.Errorf("copilot rejected the API token (status %d); the cached token was invalidated, retry to re-authenticate", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot models endpoint returned status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenResponseBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode copilot models response: %w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("copilot returned an empty model list")
	}
	sort.Strings(models)
	return models, nil
}

func (p *CopilotProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
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

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		jwt, api, err := p.token(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/chat/completions", bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create copilot stream request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+jwt)
		p.applyIntegrationHeaders(req)

		resp, err = p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("copilot unreachable at %s: %w", api, err)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			p.invalidateToken()
			if attempt == 0 {
				// One re-exchange + retry so mid-session expiry recovers.
				continue
			}
			return nil, fmt.Errorf("copilot returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		break
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("copilot returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if check := checkEventStreamContentType(resp, "copilot"); check != nil {
		return nil, check
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer resp.Body.Close()
		defer close(ch)
		streamOpenAISSE(ctx, resp.Body, ch, "copilot")
	}()

	return ch, nil
}

// Shared with gateway proxy so both send byte-identical history.
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

func buildOpenAITools(tools []ToolSpec) []openAIToolSpec {
	toolSpecs := make([]openAIToolSpec, 0, len(tools))
	for _, ts := range tools {
		ts.Parameters = validOrDefaultToolSchema(ts.Parameters)
		toolSpecs = append(toolSpecs, openAIToolSpec{
			Type:     "function",
			Function: openAIToolFunction(ts),
		})
	}
	return toolSpecs
}

// Defaults nil/empty/"null"/invalid schemas (often from MCP) to closed object; "null" makes providers reject the request.
func validOrDefaultToolSchema(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || !json.Valid(raw) {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}

// Rejects non-SSE 200s (gateway ignoring stream:true, proxy HTML, single JSON); without it streams end silently.
func checkEventStreamContentType(resp *http.Response, source string) error {
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.Contains(ct, "text/event-stream") {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	resp.Body.Close()
	return fmt.Errorf("provider %q returned a non-SSE 200 response (content-type %q): %s", source, ct, strings.TrimSpace(string(body)))
}

// emitChunk sends or reports false on cancel so a cancelled consumer can't strand the goroutine.
func emitChunk(ctx context.Context, ch chan<- StreamChunk, c StreamChunk) bool {
	select {
	case ch <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

// streamOpenAISSE parses OpenAI SSE; shared by Copilot and gateway proxy so behaviour can't drift.
func streamOpenAISSE(ctx context.Context, body io.Reader, ch chan<- StreamChunk, source string) {
	scanner := bufio.NewScanner(body)
	// Large cap; default 64 KiB truncates long tool arguments.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	type pendingToolCall struct {
		id   string
		name string
		args strings.Builder
	}
	pendingTools := make(map[int]*pendingToolCall)
	// replaced holds calls displaced by a fresh id at the same index; otherwise missing/repeated index merges two calls.
	var replaced []*pendingToolCall

	flush := func() []ToolCall {
		var calls []ToolCall
		for _, pt := range replaced {
			argsStr := pt.args.String()
			if strings.TrimSpace(argsStr) == "" {
				argsStr = "{}"
			}
			calls = append(calls, ToolCall{ID: pt.id, Name: pt.name, Args: json.RawMessage(argsStr)})
		}
		if len(pendingTools) > 0 {
			indices := make([]int, 0, len(pendingTools))
			for idx := range pendingTools {
				indices = append(indices, idx)
			}
			sort.Ints(indices)
			for _, idx := range indices {
				pt := pendingTools[idx]
				argsStr := pt.args.String()
				if strings.TrimSpace(argsStr) == "" {
					argsStr = "{}"
				}
				calls = append(calls, ToolCall{ID: pt.id, Name: pt.name, Args: json.RawMessage(argsStr)})
			}
			pendingTools = make(map[int]*pendingToolCall)
		}
		replaced = nil
		return calls
	}

	var finishReason string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			// Copilot emits named SSE events; payload is on following data: line, so skip non-data lines.
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if isTruncationFinish(finishReason) {
				if !emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("response truncated: provider stopped at the token limit (finish_reason %q)", finishReason), Done: true, FinishReason: finishReason}) {
					return
				}
				return
			}
			emitChunk(ctx, ch, StreamChunk{ToolCalls: flush(), Done: true, FinishReason: finishReason})
			return
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Error != nil {
			msg := chunk.Error.Message
			if msg == "" {
				msg = string(data)
			}
			emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("provider %q stream error: %s", source, msg), Done: true})
			return
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if !emitChunk(ctx, ch, StreamChunk{Delta: choice.Delta.Content}) {
					return
				}
			}
			if choice.Delta.ReasoningContent != "" {
				if !emitChunk(ctx, ch, StreamChunk{Reasoning: choice.Delta.ReasoningContent}) {
					return
				}
			}

			for _, tc := range choice.Delta.ToolCalls {
				idx := tc.Index
				pt, exists := pendingTools[idx]
				if !exists {
					pt = &pendingToolCall{}
					pendingTools[idx] = pt
				} else if tc.ID != "" && pt.id != "" && tc.ID != pt.id {
					// New id at same index starts a new call; old one is finished, not merged.
					replaced = append(replaced, pt)
					pt = &pendingToolCall{}
					pendingTools[idx] = pt
				} else if tc.ID == "" && pt.id == "" && tc.Function.Name != "" && pt.name != "" && tc.Function.Name != pt.name {
					// Same index, no id, different name also starts a new call.
					replaced = append(replaced, pt)
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

			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}

			if choice.FinishReason == "tool_calls" || choice.FinishReason == "stop" {
				if calls := flush(); len(calls) > 0 {
					if !emitChunk(ctx, ch, StreamChunk{ToolCalls: calls}) {
						return
					}
				}
			}
		}
	}

	if isTruncationFinish(finishReason) {
		emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("response truncated: provider stopped at the token limit (finish_reason %q)", finishReason), Done: true, FinishReason: finishReason})
		return
	}

	if calls := flush(); len(calls) > 0 {
		if !emitChunk(ctx, ch, StreamChunk{ToolCalls: calls}) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("error reading stream from %s: %w", source, err), Done: true})
	}
}

// isTruncationFinish reports token-budget cutoff ("length"/"max_tokens") vs completion.
func isTruncationFinish(reason string) bool {
	return reason == "length" || reason == "max_tokens"
}
