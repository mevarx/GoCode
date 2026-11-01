package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
)

// ExportFormat specifies the target serialization format for a session.
type ExportFormat string

const (
	FormatMarkdown ExportFormat = "markdown"
	FormatJSON     ExportFormat = "json"
)

// ExportOptions controls session serialization.
type ExportOptions struct {
	Format       ExportFormat
	IncludeTools bool
}

// JSONSessionExport represents the versioned, privacy-scrubbed JSON structure.
type JSONSessionExport struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"`
	Title     string              `json:"title"`
	Provider  string              `json:"provider"`
	Model     string              `json:"model"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	Messages  []JSONExportMessage `json:"messages"`
}

// JSONExportMessage represents a single message in the exported JSON.
type JSONExportMessage struct {
	Role       string               `json:"role"`
	Content    string               `json:"content"`
	ToolCalls  []JSONExportToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolName   string               `json:"tool_name,omitempty"`
}

// JSONExportToolCall represents a tool invocation in the exported JSON.
type JSONExportToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Export serializes record according to the specified options.
// Supported formats are FormatMarkdown (default if empty) and FormatJSON.
func Export(record *SessionRecord, options ExportOptions) ([]byte, error) {
	format := options.Format
	if format == "" {
		format = FormatMarkdown
	}

	switch format {
	case FormatMarkdown:
		s, err := ExportMarkdown(record, options)
		if err != nil {
			return nil, err
		}
		return []byte(s), nil
	case FormatJSON:
		return ExportJSON(record, options)
	default:
		return nil, fmt.Errorf("unsupported export format: %q (supported: markdown, json)", options.Format)
	}
}

// ExportJSON serializes record into pretty-printed, deterministic JSON.
// System prompts and internal reasoning content are strictly excluded.
func ExportJSON(record *SessionRecord, options ExportOptions) ([]byte, error) {
	if record == nil {
		return nil, errors.New("session record is nil")
	}

	export := JSONSessionExport{
		Version:   1,
		ID:        record.ID,
		Title:     record.Title,
		Provider:  record.Provider,
		Model:     record.Model,
		CreatedAt: record.CreatedAt.UTC(),
		UpdatedAt: record.UpdatedAt.UTC(),
		Messages:  make([]JSONExportMessage, 0),
	}

	toolNames := buildToolNameMap(record.Messages)

	for _, msg := range record.Messages {
		// SECURITY: Always exclude system prompts.
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "tool" {
			if !options.IncludeTools {
				continue
			}
			tName := toolNames[msg.ToolCallID]
			export.Messages = append(export.Messages, JSONExportMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
				ToolName:   tName,
			})
			continue
		}

		exportMsg := JSONExportMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}

		if options.IncludeTools && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				cleanArgs := sanitizeToolArgs(tc.Args)
				exportMsg.ToolCalls = append(exportMsg.ToolCalls, JSONExportToolCall{
					ID:   tc.ID,
					Name: tc.Name,
					Args: cleanArgs,
				})
			}
		}

		// When tools are excluded, an assistant message that only invoked tools without content has no user-facing text.
		if !options.IncludeTools && msg.Role == "assistant" && msg.Content == "" && len(msg.ToolCalls) > 0 {
			continue
		}

		export.Messages = append(export.Messages, exportMsg)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(export); err != nil {
		return nil, fmt.Errorf("failed to encode session JSON: %w", err)
	}

	return buf.Bytes(), nil
}

// ExportMarkdown serializes record into clean, human-readable Markdown.
// System prompts and internal reasoning content are strictly excluded.
func ExportMarkdown(record *SessionRecord, options ExportOptions) (string, error) {
	if record == nil {
		return "", errors.New("session record is nil")
	}

	var sb strings.Builder

	title := record.Title
	if strings.TrimSpace(title) == "" {
		title = record.ID
	}

	sb.WriteString(fmt.Sprintf("# Session: %s\n\n", title))
	sb.WriteString(fmt.Sprintf("- **Session ID:** `%s`\n", record.ID))
	sb.WriteString(fmt.Sprintf("- **Created:** %s\n", record.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString(fmt.Sprintf("- **Updated:** %s\n", record.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
	if record.Provider != "" {
		sb.WriteString(fmt.Sprintf("- **Provider:** `%s`\n", record.Provider))
	}
	if record.Model != "" {
		sb.WriteString(fmt.Sprintf("- **Model:** `%s`\n", record.Model))
	}

	toolNames := buildToolNameMap(record.Messages)
	hasRenderedMessages := false

	for _, msg := range record.Messages {
		// SECURITY: Always exclude system prompts.
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "tool" {
			if !options.IncludeTools {
				continue
			}
			hasRenderedMessages = true
			sb.WriteString("\n---\n\n")
			tName := toolNames[msg.ToolCallID]
			if tName != "" && msg.ToolCallID != "" {
				sb.WriteString(fmt.Sprintf("#### 📥 Tool Result: `%s` (`%s`)\n\n", tName, msg.ToolCallID))
			} else if tName != "" {
				sb.WriteString(fmt.Sprintf("#### 📥 Tool Result: `%s`\n\n", tName))
			} else if msg.ToolCallID != "" {
				sb.WriteString(fmt.Sprintf("#### 📥 Tool Result (`%s`)\n\n", msg.ToolCallID))
			} else {
				sb.WriteString("#### 📥 Tool Result\n\n")
			}

			sb.WriteString(wrapCodeFence(msg.Content, ""))
			sb.WriteString("\n")
			continue
		}

		if !options.IncludeTools && msg.Role == "assistant" && msg.Content == "" && len(msg.ToolCalls) > 0 {
			continue
		}

		hasRenderedMessages = true
		sb.WriteString("\n---\n\n")

		switch msg.Role {
		case "user":
			sb.WriteString("### 👤 User\n\n")
			sb.WriteString(msg.Content)
			sb.WriteString("\n")
		case "assistant":
			sb.WriteString("### 🤖 Assistant\n\n")
			if msg.Content != "" {
				sb.WriteString(msg.Content)
				sb.WriteString("\n")
			}
			if options.IncludeTools && len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					if msg.Content != "" {
						sb.WriteString("\n")
					}
					sb.WriteString(fmt.Sprintf("#### 🔧 Tool Call: `%s`\n\n", tc.Name))
					argsStr := formatToolArgs(tc.Args)
					sb.WriteString(wrapCodeFence(argsStr, "json"))
					sb.WriteString("\n")
				}
			}
		default:
			roleHeader := msg.Role
			if len(roleHeader) > 0 {
				roleHeader = strings.ToUpper(roleHeader[:1]) + roleHeader[1:]
			}
			sb.WriteString(fmt.Sprintf("### %s\n\n", roleHeader))
			sb.WriteString(msg.Content)
			sb.WriteString("\n")
		}
	}

	if !hasRenderedMessages {
		sb.WriteString("\n---\n\n*(No conversation messages recorded)*\n")
	}

	return sb.String(), nil
}

// buildToolNameMap creates an index of tool_call_id to tool name from assistant messages.
func buildToolNameMap(messages []provider.Message) map[string]string {
	m := make(map[string]string)
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" && tc.Name != "" {
					m[tc.ID] = tc.Name
				}
			}
		}
	}
	return m
}

// sanitizeToolArgs ensures tool arguments serialize cleanly in JSON without corruption.
func sanitizeToolArgs(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("{}")
	}
	if json.Valid(trimmed) {
		return trimmed
	}
	// Fall back to safely encoded JSON string if raw args were somehow not valid JSON.
	b, _ := json.Marshal(string(trimmed))
	return b
}

// formatToolArgs pretty-prints tool call JSON args if valid, or returns trimmed raw text.
func formatToolArgs(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "{}"
	}
	var val interface{}
	if err := json.Unmarshal(trimmed, &val); err == nil {
		pretty, err := json.MarshalIndent(val, "", "  ")
		if err == nil {
			return string(pretty)
		}
	}
	return string(trimmed)
}

// wrapCodeFence wraps text inside a Markdown code fence that dynamically expands
// if the content contains three or more consecutive backticks, preventing formatting breaks.
func wrapCodeFence(content, lang string) string {
	maxRun := 0
	curRun := 0
	for i := range len(content) {
		if content[i] == '`' {
			curRun++
			if curRun > maxRun {
				maxRun = curRun
			}
		} else {
			curRun = 0
		}
	}

	fenceLen := 3
	if maxRun >= 3 {
		fenceLen = maxRun + 1
	}
	fence := strings.Repeat("`", fenceLen)

	var sb strings.Builder
	sb.WriteString(fence)
	if lang != "" {
		sb.WriteString(lang)
	}
	sb.WriteString("\n")
	sb.WriteString(content)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(fence)
	return sb.String()
}
