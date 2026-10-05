package agent

import (
	"fmt"

	"github.com/mevarx/GoCode/internal/provider"
)

// ContextManager manages message history size, truncation, and compaction.
type ContextManager struct {
	MaxTokens int
}

// NewContextManager creates a ContextManager with the specified max tokens limit.
// If maxTokens <= 0, defaults to 8192.
func NewContextManager(maxTokens int) *ContextManager {
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	return &ContextManager{
		MaxTokens: maxTokens,
	}
}

// EstimateTokens calculates an approximate token count for a message.
func (cm *ContextManager) EstimateTokens(msg provider.Message) int {
	tokens := len(msg.Content)/4 + 4

	// Reasoning is replayed into history for reasoning providers, so it
	// occupies real request budget even though it is never displayed.
	tokens += len(msg.ReasoningContent) / 4

	for _, tc := range msg.ToolCalls {
		tokens += len(tc.Name)/4 + len(tc.Args)/4 + 4
	}

	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func (cm *ContextManager) estimateHistoryTokens(history []provider.Message) int {
	total := 0
	for _, msg := range history {
		total += cm.EstimateTokens(msg)
	}
	return total
}

// Turn represents a logical conversation turn starting with a user message
// and including all associated assistant responses, tool calls, and tool results.
type Turn struct {
	Messages []provider.Message
}

func (t Turn) Tokens(cm *ContextManager) int {
	total := 0
	for _, m := range t.Messages {
		total += cm.EstimateTokens(m)
	}
	return total
}

// groupIntoTurns groups non-system messages into turns starting at each user message.
func groupIntoTurns(messages []provider.Message) []Turn {
	var turns []Turn
	var current Turn

	for _, msg := range messages {
		if msg.Role == "user" && len(current.Messages) > 0 {
			turns = append(turns, current)
			current = Turn{Messages: []provider.Message{msg}}
		} else {
			current.Messages = append(current.Messages, msg)
		}
	}

	if len(current.Messages) > 0 {
		turns = append(turns, current)
	}

	return turns
}

// Truncate enforces MaxTokens in whole turns, preserving system message and latest turn.
// Never leaves an orphaned "tool" message at the start of the context.
func (cm *ContextManager) Truncate(history []provider.Message) []provider.Message {
	if len(history) == 0 {
		return history
	}

	if cm.estimateHistoryTokens(history) <= cm.MaxTokens {
		return history
	}

	var systemMsg *provider.Message
	nonSystem := history
	if nonSystem[0].Role == "system" {
		sys := nonSystem[0]
		systemMsg = &sys
		nonSystem = nonSystem[1:]
	}

	if len(nonSystem) == 0 {
		if systemMsg != nil {
			return []provider.Message{*systemMsg}
		}
		return nil
	}

	turns := groupIntoTurns(nonSystem)
	if len(turns) == 0 {
		if systemMsg != nil {
			return []provider.Message{*systemMsg}
		}
		return nil
	}

	sysTokens := 0
	if systemMsg != nil {
		sysTokens = cm.EstimateTokens(*systemMsg)
	}

	for len(turns) > 1 {
		totalTokens := sysTokens
		for _, t := range turns {
			totalTokens += t.Tokens(cm)
		}
		if totalTokens <= cm.MaxTokens {
			break
		}
		turns = turns[1:]
	}

	var result []provider.Message
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}
	for _, t := range turns {
		result = append(result, t.Messages...)
	}

	// Sanitization: Ensure message history never starts with an orphaned "tool" message.
	startIdx := 0
	if len(result) > 0 && result[0].Role == "system" {
		startIdx = 1
	}
	for startIdx < len(result) && result[startIdx].Role == "tool" {
		result = append(result[:startIdx], result[startIdx+1:]...)
	}

	return result
}

// Compact retains the system prompt, a summary of omitted turns, and the last N turns.
func (cm *ContextManager) Compact(history []provider.Message, keepLastTurns int) []provider.Message {
	if keepLastTurns <= 0 {
		keepLastTurns = 2
	}

	if len(history) == 0 {
		return history
	}

	var systemMsg *provider.Message
	nonSystem := history
	if nonSystem[0].Role == "system" {
		sys := nonSystem[0]
		systemMsg = &sys
		nonSystem = nonSystem[1:]
	}

	turns := groupIntoTurns(nonSystem)
	if len(turns) <= keepLastTurns {
		return history
	}

	omittedTurns := len(turns) - keepLastTurns
	omittedMsgCount := 0
	for i := 0; i < omittedTurns; i++ {
		omittedMsgCount += len(turns[i].Messages)
	}

	summaryText := fmt.Sprintf("[Earlier conversation history: %d turns (%d messages) compacted]",
		omittedTurns, omittedMsgCount)

	var result []provider.Message
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}

	result = append(result, provider.Message{
		Role:    "user",
		Content: summaryText,
	}, provider.Message{
		Role:    "assistant",
		Content: "Understood. Continuing with recent context.",
	})

	for i := omittedTurns; i < len(turns); i++ {
		result = append(result, turns[i].Messages...)
	}

	return result
}

// FormatContextStats returns a formatted status string of token usage.
func (cm *ContextManager) FormatContextStats(history []provider.Message) string {
	tokens := cm.estimateHistoryTokens(history)
	pct := float64(tokens) / float64(cm.MaxTokens) * 100.0
	return fmt.Sprintf("Context: %d / %d tokens (%.1f%%) | %d messages",
		tokens, cm.MaxTokens, pct, len(history))
}
