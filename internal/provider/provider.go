package provider

import (
	"context"
	"encoding/json"
)

// Message represents a message in conversation history.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall represents a model-initiated tool execution request.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// StreamChunk represents a chunk in a streamed model response.
type StreamChunk struct {
	Delta     string
	ToolCalls []ToolCall
	Done      bool
	Err       error
}

// ToolSpec defines a tool schema presented to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Provider defines the interface for LLM backends.
type Provider interface {
	Name() string
	Models(ctx context.Context) ([]string, error)
	Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error)
}
