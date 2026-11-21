package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Input is the stdin JSON payload. Field names follow Claude Code
// where an equivalent exists; event is kept alongside hook_event_name. Only
// set fields appear on the wire. ToolInput is emitted as a PARSED object,
// never a string; invalid JSON becomes {}.
type Input struct {
	Event                Event           `json:"event"`
	HookEventName        string          `json:"hook_event_name"`
	SessionID            string          `json:"session_id,omitempty"`
	CWD                  string          `json:"cwd,omitempty"`
	ToolName             string          `json:"tool_name,omitempty"`
	ToolInput            json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID            string          `json:"tool_use_id,omitempty"`
	ToolResponse         string          `json:"tool_response,omitempty"`
	Error                string          `json:"error,omitempty"`
	LastAssistantMessage string          `json:"last_assistant_message,omitempty"`
	Decision             string          `json:"decision,omitempty"`
	Source               string          `json:"source,omitempty"`
	Reason               string          `json:"reason,omitempty"`
	Trigger              string          `json:"trigger,omitempty"`
}

// ParseToolInput converts raw tool-call arguments into the parsed object the
// payload carries. Anything that is not a JSON object (invalid JSON, arrays,
// scalars, null) becomes {} — a hook always receives an object it can index.
func ParseToolInput(raw []byte) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return json.RawMessage("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return json.RawMessage("{}")
	}
	// Re-marshal through the map so the payload is a canonical object and
	// never carries trailing garbage after the closing brace.
	normalized, err := json.Marshal(obj)
	if err != nil {
		return json.RawMessage("{}")
	}
	return normalized
}

// marshalStdin renders the payload. Event and HookEventName are always set;
// every other field is omitted unless the caller provided it.
func marshalStdin(in Input) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(in); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// EnvAdditions returns the GOCODE_* environment variables for one firing
// including CLAUDE_PROJECT_DIR compatibility.
// Tool-specific variables are only present on tool events whose input
// carries the matching string field.
func EnvAdditions(in Input) []string {
	env := []string{"GOCODE_EVENT=" + string(in.Event)}
	add := func(name, value string) {
		if value != "" {
			env = append(env, name+"="+value)
		}
	}
	add("GOCODE_SESSION_ID", in.SessionID)
	add("GOCODE_CWD", in.CWD)
	add("GOCODE_PROJECT_DIR", in.CWD)
	add("CLAUDE_PROJECT_DIR", in.CWD)
	if in.ToolName != "" {
		add("GOCODE_TOOL_NAME", in.ToolName)
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(in.ToolInput, &obj); err == nil {
			add("GOCODE_TOOL_INPUT_COMMAND", jsonStringField(obj, "command"))
			add("GOCODE_TOOL_INPUT_FILE_PATH", jsonStringField(obj, "file_path"))
		}
	}
	return env
}

// jsonStringField reads one string-valued key from a parsed tool input.
func jsonStringField(obj map[string]json.RawMessage, key string) string {
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// stderrReason trims a hook's stderr into a reason, falling back when empty.
func stderrReason(stderr []byte, fallback string) string {
	reason := string(bytes.TrimSpace(stderr))
	if reason == "" {
		return fallback
	}
	return reason
}

// diagnosticf formats one runner diagnostic line.
func diagnosticf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
