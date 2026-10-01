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
	// ReasoningContent holds a prior assistant turn's thinking output.
	// Providers such as MiniMax and DeepSeek document that the complete
	// assistant message — including its reasoning — must be replayed into
	// history to keep the reasoning chain intact across a tool-call turn, so
	// this is stored and sent back rather than discarded after display.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type StreamChunk struct {
	Delta string
	// Reasoning carries model thinking output, which several providers
	// (MiniMax, DeepSeek) stream in a separate field from the answer. It is
	// preserved and echoed back on the next turn because those providers
	// require the full reasoning chain in history for multi-turn tool calls.
	Reasoning string
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
