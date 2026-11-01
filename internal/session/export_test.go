package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
)

func sampleRecord() *SessionRecord {
	t0 := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 10, 27, 10, 30, 0, 0, time.UTC)

	return &SessionRecord{
		ID:        "sess_test_12345",
		Title:     "Debug Concurrency Issue",
		Provider:  "anthropic",
		Model:     "claude-3-7-sonnet",
		CreatedAt: t0,
		UpdatedAt: t1,
		Messages: []provider.Message{
			{
				Role:    "system",
				Content: "INTERNAL SYSTEM PROMPT - TOP SECRET GUIDELINES - /workspace/path",
			},
			{
				Role:    "user",
				Content: "Why is the file write failing?",
			},
			{
				Role:             "assistant",
				Content:          "Let me inspect the file patch tool.",
				ReasoningContent: "THINKING CHAIN: model thoughts that must not leak",
				ToolCalls: []provider.ToolCall{
					{
						ID:   "call_001",
						Name: "file_read",
						Args: json.RawMessage(`{"path":"internal/tools/file_patch.go"}`),
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_001",
				Content:    "package tools\n\nfunc Patch() {}\n",
			},
			{
				Role:             "assistant",
				Content:          "The issue is fixed by retrying on sharing violations.",
				ReasoningContent: "THINKING CHAIN: conclusion thoughts",
			},
		},
	}
}

func TestExport_NilRecordReturnsError(t *testing.T) {
	_, err := Export(nil, ExportOptions{Format: FormatMarkdown})
	if err == nil {
		t.Fatal("expected error for nil record, got nil")
	}

	_, err = ExportMarkdown(nil, ExportOptions{})
	if err == nil {
		t.Fatal("expected error from ExportMarkdown for nil record")
	}

	_, err = ExportJSON(nil, ExportOptions{})
	if err == nil {
		t.Fatal("expected error from ExportJSON for nil record")
	}
}

func TestExport_InvalidFormatReturnsError(t *testing.T) {
	rec := sampleRecord()
	_, err := Export(rec, ExportOptions{Format: "xml"})
	if err == nil {
		t.Fatal("expected error for invalid format 'xml'")
	}
	if !strings.Contains(err.Error(), "unsupported export format") {
		t.Errorf("expected unsupported format error message, got: %v", err)
	}
}

func TestExportJSON_ValidSession(t *testing.T) {
	rec := sampleRecord()
	data, err := ExportJSON(rec, ExportOptions{IncludeTools: true})
	if err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}

	var parsed JSONSessionExport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal exported JSON: %v\nJSON:\n%s", err, string(data))
	}

	if parsed.Version != 1 {
		t.Errorf("expected Version 1, got %d", parsed.Version)
	}
	if parsed.ID != rec.ID {
		t.Errorf("expected ID %q, got %q", rec.ID, parsed.ID)
	}
	if parsed.Title != rec.Title {
		t.Errorf("expected Title %q, got %q", rec.Title, parsed.Title)
	}
	if parsed.Provider != rec.Provider {
		t.Errorf("expected Provider %q, got %q", rec.Provider, parsed.Provider)
	}
	if parsed.Model != rec.Model {
		t.Errorf("expected Model %q, got %q", rec.Model, parsed.Model)
	}

	// 5 original messages - 1 system message = 4 exported messages
	if len(parsed.Messages) != 4 {
		t.Fatalf("expected 4 messages (excluding system), got %d", len(parsed.Messages))
	}

	// 1: User
	if parsed.Messages[0].Role != "user" || parsed.Messages[0].Content != "Why is the file write failing?" {
		t.Errorf("unexpected msg 0: %+v", parsed.Messages[0])
	}

	// 2: Assistant with tool call
	if parsed.Messages[1].Role != "assistant" || len(parsed.Messages[1].ToolCalls) != 1 {
		t.Errorf("unexpected msg 1: %+v", parsed.Messages[1])
	}
	tc := parsed.Messages[1].ToolCalls[0]
	if tc.Name != "file_read" || tc.ID != "call_001" {
		t.Errorf("unexpected tool call: %+v", tc)
	}

	// 3: Tool response with resolved ToolName
	if parsed.Messages[2].Role != "tool" || parsed.Messages[2].ToolName != "file_read" || parsed.Messages[2].ToolCallID != "call_001" {
		t.Errorf("unexpected msg 2: %+v", parsed.Messages[2])
	}

	// 4: Final Assistant
	if parsed.Messages[3].Role != "assistant" || !strings.Contains(parsed.Messages[3].Content, "sharing violations") {
		t.Errorf("unexpected msg 3: %+v", parsed.Messages[3])
	}

	// SECURITY: Ensure system prompt and reasoning content are nowhere in the output
	rawJSON := string(data)
	if strings.Contains(rawJSON, "INTERNAL SYSTEM PROMPT") {
		t.Error("SECURITY VIOLATION: system prompt leaked in JSON export")
	}
	if strings.Contains(rawJSON, "THINKING CHAIN") {
		t.Error("SECURITY VIOLATION: reasoning content leaked in JSON export")
	}
}

func TestExportJSON_NoTools(t *testing.T) {
	rec := sampleRecord()
	data, err := ExportJSON(rec, ExportOptions{IncludeTools: false})
	if err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}

	var parsed JSONSessionExport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// System excluded (1), tool response excluded (1) -> 3 messages remain (user, assistant, assistant)
	if len(parsed.Messages) != 3 {
		t.Fatalf("expected 3 messages without tools, got %d", len(parsed.Messages))
	}

	for _, msg := range parsed.Messages {
		if msg.Role == "tool" {
			t.Errorf("tool message was not excluded: %+v", msg)
		}
		if len(msg.ToolCalls) > 0 {
			t.Errorf("tool calls were not excluded: %+v", msg)
		}
	}
}

func TestExportMarkdown_Formatting(t *testing.T) {
	rec := sampleRecord()
	md, err := ExportMarkdown(rec, ExportOptions{IncludeTools: true})
	if err != nil {
		t.Fatalf("ExportMarkdown failed: %v", err)
	}

	// Check header metadata
	if !strings.Contains(md, "# Session: Debug Concurrency Issue") {
		t.Errorf("missing title header in markdown:\n%s", md)
	}
	if !strings.Contains(md, "- **Session ID:** `sess_test_12345`") {
		t.Errorf("missing session ID in markdown:\n%s", md)
	}
	if !strings.Contains(md, "- **Provider:** `anthropic`") {
		t.Errorf("missing provider in markdown:\n%s", md)
	}
	if !strings.Contains(md, "- **Model:** `claude-3-7-sonnet`") {
		t.Errorf("missing model in markdown:\n%s", md)
	}

	// Check turn formatting
	if !strings.Contains(md, "### 👤 User\n\nWhy is the file write failing?") {
		t.Errorf("missing user turn in markdown:\n%s", md)
	}
	if !strings.Contains(md, "### 🤖 Assistant\n\nLet me inspect the file patch tool.") {
		t.Errorf("missing assistant turn in markdown:\n%s", md)
	}
	if !strings.Contains(md, "#### 🔧 Tool Call: `file_read`") {
		t.Errorf("missing tool call header in markdown:\n%s", md)
	}
	if !strings.Contains(md, "#### 📥 Tool Result: `file_read` (`call_001`)") {
		t.Errorf("missing tool result in markdown:\n%s", md)
	}

	// SECURITY: Ensure system prompt and reasoning content are nowhere in the output
	if strings.Contains(md, "INTERNAL SYSTEM PROMPT") {
		t.Error("SECURITY VIOLATION: system prompt leaked in markdown export")
	}
	if strings.Contains(md, "THINKING CHAIN") {
		t.Error("SECURITY VIOLATION: reasoning content leaked in markdown export")
	}
}

func TestExportMarkdown_NoTools(t *testing.T) {
	rec := sampleRecord()
	md, err := ExportMarkdown(rec, ExportOptions{IncludeTools: false})
	if err != nil {
		t.Fatalf("ExportMarkdown failed: %v", err)
	}

	if strings.Contains(md, "Tool Call:") {
		t.Error("tool call was not omitted when IncludeTools is false")
	}
	if strings.Contains(md, "Tool Result") {
		t.Error("tool result was not omitted when IncludeTools is false")
	}
	if strings.Contains(md, "package tools") {
		t.Error("tool result content was not omitted when IncludeTools is false")
	}
	if !strings.Contains(md, "### 👤 User") || !strings.Contains(md, "### 🤖 Assistant") {
		t.Error("user/assistant conversation lost when IncludeTools is false")
	}
}

func TestExport_EmptySession(t *testing.T) {
	rec := &SessionRecord{
		ID:        "sess_empty_001",
		Title:     "",
		Provider:  "openai",
		Model:     "gpt-4.1",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages:  []provider.Message{},
	}

	// Markdown empty session
	md, err := ExportMarkdown(rec, ExportOptions{})
	if err != nil {
		t.Fatalf("empty markdown export failed: %v", err)
	}
	if !strings.Contains(md, "# Session: sess_empty_001") {
		t.Errorf("expected fallback title to be session ID, got:\n%s", md)
	}
	if !strings.Contains(md, "*(No conversation messages recorded)*") {
		t.Errorf("expected empty notice in markdown:\n%s", md)
	}

	// JSON empty session
	data, err := ExportJSON(rec, ExportOptions{})
	if err != nil {
		t.Fatalf("empty json export failed: %v", err)
	}
	var parsed JSONSessionExport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if parsed.Messages == nil || len(parsed.Messages) != 0 {
		t.Errorf("expected empty non-nil messages slice, got %#v", parsed.Messages)
	}
}

func TestExport_SystemOnlySession(t *testing.T) {
	rec := &SessionRecord{
		ID:        "sess_sys_only",
		Title:     "Only System",
		Provider:  "ollama",
		Model:     "codellama",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages: []provider.Message{
			{Role: "system", Content: "internal system prompt"},
		},
	}

	md, err := ExportMarkdown(rec, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(md, "internal system prompt") {
		t.Error("system prompt leaked")
	}
	if !strings.Contains(md, "*(No conversation messages recorded)*") {
		t.Error("expected empty notice when only system prompt was present")
	}

	data, err := ExportJSON(rec, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed JSONSessionExport
	_ = json.Unmarshal(data, &parsed)
	if len(parsed.Messages) != 0 {
		t.Errorf("expected 0 messages in JSON, got %d", len(parsed.Messages))
	}
}

func TestExport_DynamicCodeFences(t *testing.T) {
	testCases := []struct {
		name         string
		content      string
		expectedOpen string
	}{
		{
			name:         "no backticks",
			content:      "simple text",
			expectedOpen: "```",
		},
		{
			name:         "three backticks inside",
			content:      "here is code:\n```go\nfunc main() {}\n```",
			expectedOpen: "````",
		},
		{
			name:         "four backticks inside",
			content:      "nested:\n````markdown\ncontent\n````",
			expectedOpen: "`````",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fence := wrapCodeFence(tc.content, "text")
			if !strings.HasPrefix(fence, tc.expectedOpen) {
				t.Errorf("expected prefix %q, got fence:\n%s", tc.expectedOpen, fence)
			}
			if !strings.HasSuffix(fence, tc.expectedOpen) {
				t.Errorf("expected suffix %q, got fence:\n%s", tc.expectedOpen, fence)
			}
		})
	}
}

func TestExport_MalformedToolArgs(t *testing.T) {
	rec := &SessionRecord{
		ID:        "sess_malformed_args",
		Title:     "Malformed Args Test",
		Provider:  "openai",
		Model:     "gpt-4.1",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages: []provider.Message{
			{
				Role:    "assistant",
				Content: "running tool with invalid json args",
				ToolCalls: []provider.ToolCall{
					{
						ID:   "call_bad",
						Name: "custom_tool",
						Args: json.RawMessage(`{not valid json}`),
					},
				},
			},
		},
	}

	// Should not error or panic
	data, err := ExportJSON(rec, ExportOptions{IncludeTools: true})
	if err != nil {
		t.Fatalf("ExportJSON failed on malformed args: %v", err)
	}

	// Must be valid JSON
	var parsed JSONSessionExport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("exported json is invalid: %v\nJSON:\n%s", err, string(data))
	}
	if len(parsed.Messages) != 1 || len(parsed.Messages[0].ToolCalls) != 1 {
		t.Fatalf("unexpected parsed messages: %+v", parsed.Messages)
	}

	md, err := ExportMarkdown(rec, ExportOptions{IncludeTools: true})
	if err != nil {
		t.Fatalf("ExportMarkdown failed on malformed args: %v", err)
	}
	if !strings.Contains(md, "{not valid json}") {
		t.Errorf("expected raw args string in markdown fence, got:\n%s", md)
	}
}

func TestExport_DefaultFormatIsMarkdown(t *testing.T) {
	rec := sampleRecord()
	data, err := Export(rec, ExportOptions{})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if !strings.HasPrefix(string(data), "# Session:") {
		t.Errorf("expected default format to be markdown, got:\n%s", string(data))
	}
}
