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

// Usage reports the token counts a provider attributes to one request. It is
// advisory: a provider that reports nothing leaves StreamChunk.Usage nil, and a
// provider that reports only one side leaves the other at zero.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// ToolCallDelta is one streamed fragment of a tool call. Index identifies the
// call inside the assistant message; ID and Name are best-effort, because some
// dialects deliver them on a later fragment (a consumer must handle empties).
// ArgsDelta is the raw JSON fragment to append to the call's argument buffer.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	ArgsDelta string
}

type StreamChunk struct {
	Delta string
	// Reasoning streams separately and is replayed next turn; MiniMax/DeepSeek require the full chain.
	Reasoning string
	ToolCalls []ToolCall
	// ToolCallDelta, when non-nil, carries one fragment of a tool call as it
	// streams. ToolCalls still arrives complete for compatibility: a consumer
	// that ignores deltas sees exactly the behaviour it saw before.
	ToolCallDelta *ToolCallDelta
	Done          bool
	Err           error
	// FinishReason is the provider stop reason; empty if unreported. "length"/"max_tokens" means truncated.
	FinishReason string
	// Usage, when non-nil, carries provider-reported token counts. Parsing is
	// always on; a provider that never reports usage leaves it nil and the
	// agent loop falls back to its local estimate.
	Usage *Usage
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
