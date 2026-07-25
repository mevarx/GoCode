package agent

import (
	"github.com/gocode-cli/gocode/internal/provider"
)

// ContextManager handles context window management and message truncation.
type ContextManager struct {
	MaxTokens int
}

// NewContextManager creates a context manager with the specified token limit.
func NewContextManager(maxTokens int) *ContextManager {
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	return &ContextManager{
		MaxTokens: maxTokens,
	}
}

// EstimateTokens calculates an estimated token count for a message.
func (cm *ContextManager) EstimateTokens(msg provider.Message) int {
	tokens := len(msg.Content)/4 + 4

	for _, tc := range msg.ToolCalls {
		tokens += len(tc.Name)/4 + len(tc.Args)/4 + 4
	}

	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// EstimateHistoryTokens returns the total estimated tokens for message history.
func (cm *ContextManager) EstimateHistoryTokens(history []provider.Message) int {
	total := 0
	for _, msg := range history {
		total += cm.EstimateTokens(msg)
	}
	return total
}

// Truncate removes oldest non-system messages until token count is within MaxTokens.
func (cm *ContextManager) Truncate(history []provider.Message) []provider.Message {
	if cm.EstimateHistoryTokens(history) <= cm.MaxTokens {
		return history
	}

	var systemMsg *provider.Message
	messages := history
	if len(messages) > 0 && messages[0].Role == "system" {
		sys := messages[0]
		systemMsg = &sys
		messages = messages[1:]
	}

	for len(messages) > 2 && cm.estimateSliceTokens(systemMsg, messages) > cm.MaxTokens {
		messages = messages[1:]
	}

	result := make([]provider.Message, 0, len(messages)+1)
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}
	return append(result, messages...)
}

func (cm *ContextManager) estimateSliceTokens(systemMsg *provider.Message, messages []provider.Message) int {
	total := 0
	if systemMsg != nil {
		total += cm.EstimateTokens(*systemMsg)
	}
	for _, msg := range messages {
		total += cm.EstimateTokens(msg)
	}
	return total
}
