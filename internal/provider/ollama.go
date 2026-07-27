package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ollama/ollama/api"
)

type OllamaProvider struct {
	client *api.Client
	host   string
}

func NewOllamaProvider(host string) (*OllamaProvider, error) {
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create ollama client: %w", err)
	}

	return &OllamaProvider{
		client: client,
		host:   host,
	}, nil
}

func (o *OllamaProvider) Name() string {
	return "ollama"
}

func (o *OllamaProvider) Models(ctx context.Context) ([]string, error) {
	resp, err := o.client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list ollama models: %w", err)
	}

	var models []string
	for _, m := range resp.Models {
		models = append(models, m.Name)
	}
	return models, nil
}

func (o *OllamaProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
	ollamaMessages := make([]api.Message, 0, len(history))
	for _, msg := range history {
		om := api.Message{
			Role:    msg.Role,
			Content: msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				var args api.ToolCallFunctionArguments
				if err := json.Unmarshal(tc.Args, &args); err != nil {
					return nil, fmt.Errorf("failed to unmarshal tool call args: %w", err)
				}
				om.ToolCalls = append(om.ToolCalls, api.ToolCall{
					Function: api.ToolCallFunction{
						Name:      tc.Name,
						Arguments: args,
					},
				})
			}
		}
		ollamaMessages = append(ollamaMessages, om)
	}

	ollamaTools := make([]api.Tool, 0, len(tools))
	for _, ts := range tools {
		var params api.ToolFunctionParameters
		if err := json.Unmarshal(ts.Parameters, &params); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool params for %s: %w", ts.Name, err)
		}
		ollamaTools = append(ollamaTools, api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        ts.Name,
				Description: ts.Description,
				Parameters:  params,
			},
		})
	}

	ch := make(chan StreamChunk, 64)

	req := &api.ChatRequest{
		Model:    model,
		Messages: ollamaMessages,
	}
	if len(ollamaTools) > 0 {
		req.Tools = ollamaTools
	}

	go func() {
		defer close(ch)

		err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
			chunk := StreamChunk{
				Delta: resp.Message.Content,
				Done:  resp.Done,
			}

			if len(resp.Message.ToolCalls) > 0 {
				for i, tc := range resp.Message.ToolCalls {
					argsJSON, err := json.Marshal(tc.Function.Arguments)
					if err != nil {
						chunk.Err = fmt.Errorf("failed to marshal tool call args: %w", err)
						ch <- chunk
						return err
					}
					chunk.ToolCalls = append(chunk.ToolCalls, ToolCall{
						ID:   fmt.Sprintf("call_%s_%d", tc.Function.Name, i),
						Name: tc.Function.Name,
						Args: argsJSON,
					})
				}
			}

			ch <- chunk
			return nil
		})

		if err != nil {
			ch <- StreamChunk{Err: err, Done: true}
		}
	}()

	return ch, nil
}
