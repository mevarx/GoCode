package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mevarx/GoCode/internal/config"
)

// AnthropicProvider implements Provider for the native Anthropic Messages API.
type AnthropicProvider struct {
	name   string
	cfg    config.GatewayConfig
	client *http.Client
}

// NewAnthropicProvider initializes an AnthropicProvider instance.
func NewAnthropicProvider(cfg config.GatewayConfig) *AnthropicProvider {
	return &AnthropicProvider{
		name:   "anthropic",
		cfg:    cfg,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// Name returns the provider name identifier.
func (p *AnthropicProvider) Name() string {
	return p.name
}

// getAPIKey resolves the API key from config or environment variable.
func (p *AnthropicProvider) getAPIKey() string {
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

// Models returns available Anthropic model identifiers.
// Anthropic does not expose a /models endpoint, so we return a static list.
func (p *AnthropicProvider) Models(ctx context.Context) ([]string, error) {
	return []string{
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-3-5-sonnet-20241022",
		"claude-3-5-haiku-20241022",
		"claude-3-opus-20240229",
	}, nil
}

// --- Anthropic API types ---

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string                `json:"role"`
	Content json.RawMessage       `json:"content"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// --- Anthropic SSE event types ---

type anthropicStreamEvent struct {
	Type  string          `json:"type"`
	Index int             `json:"index,omitempty"`
	Delta json.RawMessage `json:"delta,omitempty"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`
}

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// Stream sends a chat completion request using the Anthropic Messages API with SSE streaming.
func (p *AnthropicProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	url := baseURL + "/messages"

	// Separate system message from conversation history.
	var systemPrompt string
	var convMessages []Message
	for _, msg := range history {
		if msg.Role == "system" {
			systemPrompt = msg.Content
		} else {
			convMessages = append(convMessages, msg)
		}
	}

	// Convert messages to Anthropic format.
	anthropicMsgs := make([]anthropicMessage, 0, len(convMessages))
	for _, msg := range convMessages {
		switch msg.Role {
		case "user":
			content, _ := json.Marshal(msg.Content)
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "user",
				Content: content,
			})

		case "assistant":
			blocks := []anthropicContentBlock{}
			if msg.Content != "" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Args,
				})
			}
			content, _ := json.Marshal(blocks)
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "assistant",
				Content: content,
			})

		case "tool":
			blocks := []anthropicContentBlock{{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}}
			content, _ := json.Marshal(blocks)
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "user",
				Content: content,
			})
		}
	}

	// Build request body.
	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 8192,
		System:    systemPrompt,
		Messages:  anthropicMsgs,
		Stream:    true,
	}

	if len(tools) > 0 {
		anthropicTools := make([]anthropicTool, 0, len(tools))
		for _, ts := range tools {
			anthropicTools = append(anthropicTools, anthropicTool{
				Name:        ts.Name,
				Description: ts.Description,
				InputSchema: ts.Parameters,
			})
		}
		reqBody.Tools = anthropicTools
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal anthropic request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create anthropic stream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	apiKey := p.getAPIKey()
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q unreachable at %s: %w", p.name, p.cfg.BaseURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider %q returned status %d: %s", p.name, resp.StatusCode, string(body))
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)

		// Track pending tool calls by content block index.
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
			if data == "" {
				continue
			}

			var event anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "content_block_start":
				if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
					pendingTools[event.Index] = &pendingToolCall{
						id:   event.ContentBlock.ID,
						name: event.ContentBlock.Name,
					}
				}

			case "content_block_delta":
				var delta anthropicDelta
				if err := json.Unmarshal(event.Delta, &delta); err != nil {
					continue
				}

				switch delta.Type {
				case "text_delta":
					if delta.Text != "" {
						ch <- StreamChunk{Delta: delta.Text}
					}
				case "input_json_delta":
					if pt, ok := pendingTools[event.Index]; ok {
						pt.args.WriteString(delta.PartialJSON)
					}
				}

			case "content_block_stop":
				if pt, ok := pendingTools[event.Index]; ok {
					ch <- StreamChunk{
						ToolCalls: []ToolCall{{
							ID:   pt.id,
							Name: pt.name,
							Args: json.RawMessage(pt.args.String()),
						}},
					}
					delete(pendingTools, event.Index)
				}

			case "message_stop":
				ch <- StreamChunk{Done: true}
				return

			case "error":
				ch <- StreamChunk{
					Err:  fmt.Errorf("anthropic stream error: %s", string(event.Delta)),
					Done: true,
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Err: fmt.Errorf("error reading stream from anthropic: %w", err), Done: true}
		}
	}()

	return ch, nil
}
