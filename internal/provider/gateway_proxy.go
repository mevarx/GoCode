package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mevarx/GoCode/internal/config"
)

type GatewayProxyProvider struct {
	name   string
	cfg    config.GatewayConfig
	client *http.Client
}

func NewGatewayProxyProvider(name string, cfg config.GatewayConfig) *GatewayProxyProvider {
	baseTransport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &GatewayProxyProvider{
		name: name,
		cfg:  cfg,
		client: &http.Client{
			Transport: NewRetryTransport(baseTransport),
		},
	}
}

func (p *GatewayProxyProvider) Name() string {
	return p.name
}

func (p *GatewayProxyProvider) getAPIKey() string {
	if p.cfg.APIKey != "" {
		return p.cfg.APIKey
	}
	if p.cfg.APIKeyEnv != "" {
		if key := os.Getenv(p.cfg.APIKeyEnv); key != "" {
			return key
		}
	}
	return ""
}

func (p *GatewayProxyProvider) setCustomHeaders(req *http.Request) {
	if apiKey := p.getAPIKey(); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if p.name == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/mevarx/GoCode")
		req.Header.Set("X-Title", "GoCode")
	}
}

func (p *GatewayProxyProvider) Models(ctx context.Context) ([]string, error) {
	url := strings.TrimRight(p.cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", p.name, err)
	}

	p.setCustomHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q unreachable at %s: %w", p.name, p.cfg.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gateway %q returned status %d: %s", p.name, resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode models response from %s: %w", p.name, err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}

	return models, nil
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	// ReasoningContent is replayed on assistant turns so providers that
	// thread reasoning through the request (MiniMax, DeepSeek) keep the
	// chain continuous across a tool-call round trip.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolSpec struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// ReasoningContent is the DeepSeek-style thinking field used by
			// MiniMax, DeepSeek and Hermes-compatible endpoints.
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (p *GatewayProxyProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
	url := strings.TrimRight(p.cfg.BaseURL, "/") + "/chat/completions"

	payload := map[string]interface{}{
		"model":    model,
		"messages": buildOpenAIMessages(history),
		"stream":   true,
	}

	if len(tools) > 0 {
		payload["tools"] = buildOpenAITools(tools)
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create stream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	p.setCustomHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q unreachable at %s: %w", p.name, p.cfg.BaseURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("gateway %q returned status %d: %s", p.name, resp.StatusCode, string(body))
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer resp.Body.Close()
		defer close(ch)
		streamOpenAISSE(resp.Body, ch, p.name)
	}()

	return ch, nil
}
