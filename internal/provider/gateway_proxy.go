package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mevarx/GoCode/internal/config"
)

// GatewayProxyProvider implements Provider for OpenAI-compatible gateway proxies like OmniRoute.
type GatewayProxyProvider struct {
	name   string
	cfg    config.GatewayConfig
	client *http.Client
}

// NewGatewayProxyProvider initializes a GatewayProxyProvider instance.
func NewGatewayProxyProvider(name string, cfg config.GatewayConfig) *GatewayProxyProvider {
	return &GatewayProxyProvider{
		name:   name,
		cfg:    cfg,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// Name returns the provider name identifier.
func (p *GatewayProxyProvider) Name() string {
	return p.name
}

// BaseURL returns the configured base URL.
func (p *GatewayProxyProvider) BaseURL() string {
	return p.cfg.BaseURL
}

// DefaultModel returns the configured default model.
func (p *GatewayProxyProvider) DefaultModel() string {
	return p.cfg.DefaultModel
}

// Models fetches available model IDs from the gateway's /v1/models endpoint.
func (p *GatewayProxyProvider) Models(ctx context.Context) ([]string, error) {
	url := strings.TrimRight(p.cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", p.name, err)
	}

	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway %q unreachable at %s — is it running? (npm install -g %s && %s): %w", p.name, p.cfg.BaseURL, p.name, p.name, err)
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
			Content   string `json:"content"`
			ToolCalls []struct {
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

// Stream sends a chat completion request and streams response chunks using SSE.
func (p *GatewayProxyProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
	url := strings.TrimRight(p.cfg.BaseURL, "/") + "/chat/completions"

	messages := make([]openAIMessage, 0, len(history))
	for _, msg := range history {
		oMsg := openAIMessage{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
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

	payload := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}

	if len(tools) > 0 {
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
		payload["tools"] = toolSpecs
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
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway %q unreachable at %s — is it running? (npm install -g %s && %s): %w", p.name, p.cfg.BaseURL, p.name, p.name, err)
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

		scanner := bufio.NewScanner(resp.Body)
		// Accumulate tool call argument chunks indexed by tool call position
		type pendingToolCall struct {
			id   string
			name string
			args strings.Builder
		}
		pendingTools := make(map[int]*pendingToolCall)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			if !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				// Flush any pending tool calls before completion
				var finalToolCalls []ToolCall
				for _, pt := range pendingTools {
					finalToolCalls = append(finalToolCalls, ToolCall{
						ID:   pt.id,
						Name: pt.name,
						Args: json.RawMessage(pt.args.String()),
					})
				}
				ch <- StreamChunk{
					ToolCalls: finalToolCalls,
					Done:      true,
				}
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

				for _, tc := range choice.Delta.ToolCalls {
					idx := tc.Index
					pt, exists := pendingTools[idx]
					if !exists {
						pt = &pendingToolCall{
							id:   tc.ID,
							name: tc.Function.Name,
						}
						if pt.id == "" {
							pt.id = fmt.Sprintf("call_%s_%d", tc.Function.Name, idx)
						}
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
				}

				if choice.FinishReason == "tool_calls" || choice.FinishReason == "stop" {
					if len(pendingTools) > 0 {
						var toolCalls []ToolCall
						for _, pt := range pendingTools {
							toolCalls = append(toolCalls, ToolCall{
								ID:   pt.id,
								Name: pt.name,
								Args: json.RawMessage(pt.args.String()),
							})
						}
						pendingTools = make(map[int]*pendingToolCall)
						ch <- StreamChunk{
							ToolCalls: toolCalls,
						}
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Err: fmt.Errorf("error reading stream from %s: %w", p.name, err), Done: true}
		}
	}()

	return ch, nil
}
