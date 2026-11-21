package hooks

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The stdin payload carries ONLY set fields, event alongside hook_event_name,
// and tool_input as a parsed object.
func TestStdinPayloadGoldenShape(t *testing.T) {
	in := Input{
		Event:         PreToolUse,
		HookEventName: string(PreToolUse),
		SessionID:     "s-1",
		CWD:           "/work",
		ToolName:      "bash",
		ToolInput:     json.RawMessage(`{"command":"ls"}`),
		ToolUseID:     "call_1",
	}
	data, err := marshalStdin(in)
	if err != nil {
		t.Fatalf("marshalStdin: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	want := map[string]any{
		"event":           "PreToolUse",
		"hook_event_name": "PreToolUse",
		"session_id":      "s-1",
		"cwd":             "/work",
		"tool_name":       "bash",
		"tool_input":      map[string]any{"command": "ls"},
		"tool_use_id":     "call_1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload = %#v, want %#v", got, want)
	}
	// Unset fields must not appear at all.
	for _, absent := range []string{"tool_response", "error", "last_assistant_message", "decision", "source", "reason", "trigger"} {
		if _, ok := got[absent]; ok {
			t.Errorf("unset field %q appeared in the payload", absent)
		}
	}
}

func TestParseToolInput(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"object", `{"command":"ls","n":1}`, `{"command":"ls","n":1}`},
		{"invalid JSON", `{"command":`, `{}`},
		{"empty", ``, `{}`},
		{"array is not an object", `[1,2]`, `{}`},
		{"scalar is not an object", `"ls"`, `{}`},
		{"null is not an object", `null`, `{}`},
		{"trailing garbage", `{"a":1} garbage`, `{}`},
	}
	for _, tc := range cases {
		got := ParseToolInput([]byte(tc.raw))
		var gotObj, wantObj map[string]any
		if err := json.Unmarshal(got, &gotObj); err != nil {
			t.Errorf("%s: result %q is not an object: %v", tc.name, got, err)
			continue
		}
		_ = json.Unmarshal([]byte(tc.want), &wantObj)
		if !reflect.DeepEqual(gotObj, wantObj) {
			t.Errorf("%s: ParseToolInput(%q) = %s, want %s", tc.name, tc.raw, got, tc.want)
		}
	}
}

// Env additions use GOCODE_* names plus CLAUDE_PROJECT_DIR compat; tool
// input variables only appear when the input carries the string field.
func TestEnvAdditions(t *testing.T) {
	in := Input{
		Event:     PreToolUse,
		SessionID: "s-1",
		CWD:       "/work",
		ToolName:  "bash",
		ToolInput: json.RawMessage(`{"command":"ls -la","file_path":"/work/a.txt"}`),
	}
	got := map[string]string{}
	for _, entry := range EnvAdditions(in) {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("bad env entry %q", entry)
		}
		got[k] = v
	}
	want := map[string]string{
		"GOCODE_EVENT":                "PreToolUse",
		"GOCODE_SESSION_ID":           "s-1",
		"GOCODE_CWD":                  "/work",
		"GOCODE_PROJECT_DIR":          "/work",
		"CLAUDE_PROJECT_DIR":          "/work",
		"GOCODE_TOOL_NAME":            "bash",
		"GOCODE_TOOL_INPUT_COMMAND":   "ls -la",
		"GOCODE_TOOL_INPUT_FILE_PATH": "/work/a.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %#v, want %#v", got, want)
	}

	// Lifecycle event: no tool variables, no session/cwd when unset.
	bare := EnvAdditions(Input{Event: SessionEnd, Reason: "exit"})
	if len(bare) != 1 || bare[0] != "GOCODE_EVENT=SessionEnd" {
		t.Errorf("bare lifecycle env = %v, want only GOCODE_EVENT", bare)
	}

	// Non-string command field contributes no variable.
	numeric := EnvAdditions(Input{Event: PreToolUse, ToolName: "x", ToolInput: json.RawMessage(`{"command":42}`)})
	for _, entry := range numeric {
		if entry == "GOCODE_TOOL_INPUT_COMMAND=42" {
			t.Error("non-string command leaked into the environment")
		}
	}
}
