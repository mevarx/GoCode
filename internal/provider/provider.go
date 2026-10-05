package provider

import (
	"context"
	"encoding/json"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// ReasoningContent must be replayed into history; MiniMax/DeepSeek require the full chain across tool-call turns.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type StreamChunk struct {
	Delta string
	// Reasoning streams separately and is replayed next turn; MiniMax/DeepSeek require the full chain.
	Reasoning string
	ToolCalls []ToolCall
	Done      bool
	Err       error
	// FinishReason is the provider stop reason; empty if unreported. "length"/"max_tokens" means truncated.
	FinishReason string
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Provider interface {
	Name() string
	Models(ctx context.Context) ([]string, error)
	Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error)
}
