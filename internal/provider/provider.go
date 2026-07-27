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
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type StreamChunk struct {
	Delta     string
	ToolCalls []ToolCall
	Done      bool
	Err       error
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
