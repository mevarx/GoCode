package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// ContextManager manages message history size, truncation, and compaction.
type ContextManager struct {
	MaxTokens int
	// ConfiguredMaxTokens is the budget the caller asked for. Zero means the
	// budget is resolved from the active model's context window each round.
	ConfiguredMaxTokens int
	// Compactions counts every compacted history, so a turn's metrics can
	// report the delta without threading a counter through commands.
	Compactions atomic.Int64
}

// NewContextManager creates a ContextManager with the specified max tokens limit.
// If maxTokens <= 0, the budget is resolved from the model's context window by
// ResolveBudget; until then MaxTokens defaults to 8192.
func NewContextManager(maxTokens int) *ContextManager {
	configured := maxTokens
	if configured < 0 {
		configured = 0
	}
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	return &ContextManager{
		MaxTokens:           maxTokens,
		ConfiguredMaxTokens: configured,
	}
}

// ResolveBudget picks the token budget for model and stores it in MaxTokens.
// An explicit configured budget always wins. Otherwise the budget is 80% of the
// model's context window, clamped to [4096, window], leaving headroom for the
// response. The loop calls this before every Truncate, so switching models
// takes effect on the next provider round.
func (cm *ContextManager) ResolveBudget(reg *provider.Registry, model string) int {
	if cm == nil {
		return 8192
	}
	if cm.ConfiguredMaxTokens > 0 {
		cm.MaxTokens = cm.ConfiguredMaxTokens
		return cm.MaxTokens
	}

	window := 0
	if reg != nil {
		window = reg.ContextWindow(model)
	}
	if window <= 0 {
		// Keep the package fallback in one place in the provider registry.
		window = 65536
	}

	budget := int(float64(window) * 0.8)
	if budget < 4096 {
		budget = 4096
	}
	if window > 0 && budget > window {
		budget = window
	}
	cm.MaxTokens = budget
	return budget
}

// estimateTextTokens approximates the token count of text without a tokenizer.
// One token per four ASCII letters/digits matches English prose closely, while
// CJK scripts (roughly one token per 1.5 runes) and punctuation-heavy code are
// weighted up so source, JSON, and paths are not undercounted. Empty text is
// zero; any other text costs at least one token.
func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}

	total := 0.0
	for _, r := range s {
		switch {
		case isCJKRune(r):
			total += 1.0 / 1.5
		case r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			total += 1.0 / 4.0
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			total += 1.0 / 6.0
		default:
			// Code punctuation and non-ASCII symbols tokenize worse than prose.
			total += 1.0 / 3.0
		}
	}

	tokens := int(math.Ceil(total))
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// isCJKRune reports whether r belongs to a script that typically tokenizes at
// close to one token per character: Han, Hangul, and Japanese kana.
func isCJKRune(r rune) bool {
	switch {
	case r >= 0x3040 && r <= 0x30FF: // Hiragana and Katakana
		return true
	case r >= 0x31F0 && r <= 0x31FF: // Katakana phonetic extensions
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Unified Ideographs Extension A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // Hangul Syllables
		return true
	case r >= 0x1100 && r <= 0x11FF: // Hangul Jamo
		return true
	default:
		return false
	}
}

// EstimateTokens calculates an approximate token count for a message.
func (cm *ContextManager) EstimateTokens(msg provider.Message) int {
	tokens := estimateTextTokens(msg.Content) + 4

	// Reasoning is replayed into history for reasoning providers, so it
	// occupies real request budget even though it is never displayed.
	tokens += estimateTextTokens(msg.ReasoningContent)

	for _, tc := range msg.ToolCalls {
		tokens += estimateTextTokens(tc.Name) + estimateTextTokens(string(tc.Args)) + 4
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

// syntheticToolResult closes an assistant tool call whose real result is not in
// the window any more (truncation or compaction cut it out).
const syntheticToolResult = "Error: result missing (history truncated)"

// splitSystemMessages separates system messages from history, merging them in
// order into one message. Providers accept at most one system prompt and only
// at the front, so callers group and trim only conversational turns.
func splitSystemMessages(history []provider.Message) (*provider.Message, []provider.Message) {
	var parts []string
	rest := make([]provider.Message, 0, len(history))
	for _, msg := range history {
		if msg.Role == "system" {
			if msg.Content != "" {
				parts = append(parts, msg.Content)
			}
			continue
		}
		rest = append(rest, msg)
	}
	if len(parts) == 0 {
		return nil, rest
	}
	system := provider.Message{Role: "system", Content: strings.Join(parts, "\n\n")}
	return &system, rest
}

// EnsureToolPairing repairs history so it satisfies the provider wire
// invariant. OpenAI-shaped and Anthropic APIs reject histories that violate it:
//
//  1. at most one system message, and it is at index 0; any further system
//     messages are merged into it rather than dropped;
//  2. every tool message directly follows an assistant tool_calls message that
//     declares its ToolCallID, and every assistant tool_calls message is
//     followed by exactly one tool result per declared call, in call order;
//  3. tool messages no assistant declares are dropped, and calls without a
//     result get a synthetic result at the call's position, so results always
//     follow the declared call order.
//
// Assistant text content is never dropped. The returned slice is new; the
// input is not modified.
func EnsureToolPairing(history []provider.Message) []provider.Message {
	if len(history) == 0 {
		return nil
	}

	systemMsg, rest := splitSystemMessages(history)

	out := make([]provider.Message, 0, len(history)+1)
	if systemMsg != nil {
		out = append(out, *systemMsg)
	}

	for i := 0; i < len(rest); i++ {
		msg := rest[i]

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Results for a call are the contiguous run of tool messages after
			// the assistant message; that is where providers require them.
			end := i + 1
			for end < len(rest) && rest[end].Role == "tool" {
				end++
			}
			results := rest[i+1 : end]

			out = append(out, msg)

			// Emit one result per call, in call order. Each result is used at
			// most once so a duplicate cannot stand in for two calls; a call
			// whose result was cut out gets a synthetic result in its place.
			used := make([]bool, len(results))
			for _, call := range msg.ToolCalls {
				found := -1
				for k := range results {
					if !used[k] && results[k].ToolCallID == call.ID {
						found = k
						break
					}
				}
				if found < 0 {
					out = append(out, provider.Message{
						Role:       "tool",
						Content:    syntheticToolResult,
						ToolCallID: call.ID,
					})
					continue
				}
				used[found] = true
				out = append(out, results[found])
			}

			i = end - 1
			continue
		}

		if msg.Role == "tool" {
			// Any tool message not consumed above has no declaring assistant.
			continue
		}

		out = append(out, msg)
	}

	return out
}

// keepLastTurns keeps the system prompt plus the last keepTurns logical turns.
// A turn is never split, so a tool call and its results stay together.
// It returns messages unchanged when nothing would be dropped.
func keepLastTurns(messages []provider.Message, keepTurns int) []provider.Message {
	if keepTurns <= 0 || len(messages) == 0 {
		return messages
	}

	systemMsg, rest := splitSystemMessages(messages)
	turns := groupIntoTurns(rest)
	if len(turns) <= keepTurns {
		return messages
	}

	out := make([]provider.Message, 0, len(messages))
	if systemMsg != nil {
		out = append(out, *systemMsg)
	}
	for _, turn := range turns[len(turns)-keepTurns:] {
		out = append(out, turn.Messages...)
	}
	return out
}

// inFlightToolCallStart returns the index of the assistant message that begins
// a tool-call block whose results are still being written, if history ends
// inside such a block. Such a tail must not be repaired by synthesizing
// results: the real ones arrive through later AddMessage calls and would become
// orphans.
func inFlightToolCallStart(history []provider.Message) (int, bool) {
	i := len(history) - 1
	trailing := 0
	for i >= 0 && history[i].Role == "tool" {
		trailing++
		i--
	}
	if i < 0 {
		return 0, false
	}
	last := history[i]
	if last.Role != "assistant" || len(last.ToolCalls) == 0 || trailing >= len(last.ToolCalls) {
		return 0, false
	}
	return i, true
}

func inFlightToolCalls(history []provider.Message) bool {
	_, ok := inFlightToolCallStart(history)
	return ok
}

// keepDeclaredResults keeps an in-flight assistant message and the results it
// already has, dropping results its calls do not declare. Missing results stay
// missing: the caller is still writing them.
func keepDeclaredResults(block []provider.Message) []provider.Message {
	if len(block) == 0 || block[0].Role != "assistant" || len(block[0].ToolCalls) == 0 {
		return block
	}
	assistant := block[0]
	out := make([]provider.Message, 0, len(block))
	out = append(out, assistant)
	used := make([]bool, len(assistant.ToolCalls))
	for _, msg := range block[1:] {
		if msg.Role != "tool" {
			continue
		}
		for k, call := range assistant.ToolCalls {
			if used[k] || call.ID != msg.ToolCallID {
				continue
			}
			used[k] = true
			out = append(out, msg)
			break
		}
	}
	return out
}

// trimHistory applies the session trim policy: keep the last keepTurns logical
// turns and then re-establish the tool-pairing invariant.
//
// Only the caller's live turn may legitimately lack results, and only the input
// itself can identify it. The trimmed window cannot: hoisting system messages
// can expose an older, malformed assistant call at the window's end, and that
// one must be settled rather than mistaken for a turn still in progress.
func trimHistory(messages []provider.Message, keepTurns int) []provider.Message {
	trimmed := keepLastTurns(messages, keepTurns)

	if !inFlightToolCalls(messages) {
		return EnsureToolPairing(trimmed)
	}
	start, ok := inFlightToolCallStart(trimmed)
	if !ok {
		return EnsureToolPairing(trimmed)
	}

	// Repair the settled prefix so trimming cannot strand a result there, then
	// keep the live block's real results and let its remaining ones land.
	out := EnsureToolPairing(trimmed[:start])
	out = append(out, keepDeclaredResults(trimmed[start:])...)
	return out
}

// Truncate enforces MaxTokens in whole turns, preserving system message and latest turn.
// It always returns history satisfying EnsureToolPairing's invariant: resumed
// transcripts and compaction can leave orphaned calls or results behind.
func (cm *ContextManager) Truncate(history []provider.Message) []provider.Message {
	if len(history) == 0 {
		return history
	}

	// Pairing is a wire requirement, so repair even when the budget already fits.
	if cm.estimateHistoryTokens(history) <= cm.MaxTokens {
		return EnsureToolPairing(history)
	}

	systemMsg, nonSystem := splitSystemMessages(history)
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

	result := make([]provider.Message, 0, len(history))
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}
	for _, t := range turns {
		result = append(result, t.Messages...)
	}

	return EnsureToolPairing(result)
}

// Compact retains the system prompt, a summary of omitted turns, and the last N turns.
// The result satisfies EnsureToolPairing's invariant even when an omitted turn
// boundary splits a call from its result.
func (cm *ContextManager) Compact(history []provider.Message, keepLastTurns int) []provider.Message {
	if keepLastTurns <= 0 {
		keepLastTurns = 2
	}

	if len(history) == 0 {
		return history
	}

	systemMsg, nonSystem := splitSystemMessages(history)
	turns := groupIntoTurns(nonSystem)
	if len(turns) <= keepLastTurns {
		return EnsureToolPairing(history)
	}

	cm.Compactions.Add(1)

	omittedTurns := len(turns) - keepLastTurns
	omittedMsgCount := 0
	for i := range omittedTurns {
		omittedMsgCount += len(turns[i].Messages)
	}

	summaryText := fmt.Sprintf("[Earlier conversation history: %d turns (%d messages) compacted]",
		omittedTurns, omittedMsgCount)

	result := make([]provider.Message, 0, len(history))
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

	return EnsureToolPairing(result)
}

// Compaction summary limits. The transcript is bounded so the summary request
// itself cannot overflow the model window it is meant to relieve; the timeout
// keeps a stalled provider from hanging /compact.
const (
	compactionTranscriptLimit = 20000
	compactionSummaryTimeout  = 60 * time.Second
	compactionSummaryHeading  = "[Summary of earlier conversation]"
	compactionSummaryAck      = "Understood. Continuing from this summary."
)

const compactionSummaryPrompt = `You condense a coding-assistant conversation so work can continue in a smaller context window.
Write a concise summary (bullet points preferred) of:
- the user's current goal and explicit requirements
- decisions made, with brief rationale
- files read, created, or modified (paths)
- constraints, errors, and unresolved problems
- concrete next steps
Merge any [Summary of earlier conversation] already present in the transcript.
Do not invent work, do not continue the conversation, and do not include credentials.
Return only the summary text.

Transcript:
`

// CompactWithSummary replaces older turns with a model-written summary of
// them. It keeps the system prompt, a summary pair, and the last keepLastTurns
// turns. Unlike Compact, the summary records decisions and open files instead
// of only counting what was dropped, so the model can continue coherently.
//
// The provider call is bounded; an error or an empty answer is returned to the
// caller, which is expected to fall back to Compact.
func (cm *ContextManager) CompactWithSummary(ctx context.Context, p provider.Provider, model string, history []provider.Message, keepLastTurns int) ([]provider.Message, error) {
	if keepLastTurns <= 0 {
		keepLastTurns = 2
	}
	if len(history) == 0 {
		return history, nil
	}

	systemMsg, nonSystem := splitSystemMessages(history)
	turns := groupIntoTurns(nonSystem)
	if len(turns) <= keepLastTurns {
		return EnsureToolPairing(history), nil
	}
	if p == nil {
		return nil, fmt.Errorf("compact: no provider available for summary")
	}

	omitted := turns[:len(turns)-keepLastTurns]
	prompt := compactionSummaryPrompt + boundCompactionTranscript(buildCompactionTranscript(omitted))

	sctx, cancel := context.WithTimeout(ctx, compactionSummaryTimeout)
	defer cancel()

	ch, err := p.Stream(sctx, model, []provider.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		return nil, fmt.Errorf("compact: summary request failed: %w", err)
	}

	var sb strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return nil, fmt.Errorf("compact: summary stream failed: %w", chunk.Err)
		}
		// Tool calls in a summary response are ignored: this is a text-only
		// request and must not execute anything.
		if chunk.Delta != "" {
			sb.WriteString(chunk.Delta)
		}
	}

	summary := strings.TrimSpace(sb.String())
	if summary == "" {
		return nil, fmt.Errorf("compact: provider returned an empty summary")
	}

	cm.Compactions.Add(1)

	result := make([]provider.Message, 0, len(history))
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}

	result = append(result,
		provider.Message{Role: "user", Content: compactionSummaryHeading + "\n" + summary},
		provider.Message{Role: "assistant", Content: compactionSummaryAck},
	)

	for i := len(turns) - keepLastTurns; i < len(turns); i++ {
		result = append(result, turns[i].Messages...)
	}

	// An omitted turn boundary can split a call from its result; repair the
	// window before it reaches a provider.
	return EnsureToolPairing(result), nil
}

// buildCompactionTranscript renders omitted turns as plain role-tagged text.
// Tool output and tool-call arguments pass through the transcript redaction
// boundary exactly like a tool result shown to the model.
func buildCompactionTranscript(turns []Turn) string {
	var sb strings.Builder
	for _, turn := range turns {
		for _, msg := range turn.Messages {
			if msg.Role == "assistant" {
				if msg.Content != "" {
					sb.WriteString("assistant: ")
					sb.WriteString(msg.Content)
					sb.WriteByte('\n')
				}
				for _, tc := range msg.ToolCalls {
					args, _ := tools.RedactToolOutput(string(tc.Args))
					sb.WriteString("assistant tool_call ")
					sb.WriteString(tc.Name)
					sb.WriteString("(")
					sb.WriteString(args)
					sb.WriteString(")\n")
				}
				continue
			}

			if msg.Role == "tool" {
				output, _ := tools.RedactToolOutput(msg.Content)
				sb.WriteString("tool[")
				sb.WriteString(msg.ToolCallID)
				sb.WriteString("]: ")
				sb.WriteString(output)
				sb.WriteByte('\n')
				continue
			}

			sb.WriteString(msg.Role)
			sb.WriteString(": ")
			sb.WriteString(msg.Content)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// boundCompactionTranscript caps the transcript at compactionTranscriptLimit
// runes, keeping the end (the most recent omitted context) rather than the
// beginning. Cutting on a rune boundary keeps the prompt valid UTF-8.
func boundCompactionTranscript(transcript string) string {
	runes := []rune(transcript)
	if len(runes) <= compactionTranscriptLimit {
		return transcript
	}
	return "… (earlier transcript omitted)\n" + string(runes[len(runes)-compactionTranscriptLimit:])
}

// FormatContextStats returns a formatted status string of token usage.
func (cm *ContextManager) FormatContextStats(history []provider.Message) string {
	tokens := cm.estimateHistoryTokens(history)
	pct := float64(tokens) / float64(cm.MaxTokens) * 100.0
	return fmt.Sprintf("Context: %d / %d tokens (%.1f%%) | %d messages",
		tokens, cm.MaxTokens, pct, len(history))
}
