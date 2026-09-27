package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

// newAnthropicTestProvider points an AnthropicProvider at a test server.
func newAnthropicTestProvider(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func anthropicProviderFor(baseURL string) *AnthropicProvider {
	return NewAnthropicProvider(config.GatewayConfig{
		BaseURL:   baseURL,
		APIKey:    "test-key",
		APIKeyEnv: "",
	})
}

func sseLine(payload string) string {
	return "data: " + payload + "\n\n"
}

// A text-only response must surface deltas and terminate.
func TestAnthropicStream_TextOnly(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, s := range []string{"Hello", " world"} {
			fmt.Fprint(w, sseLine(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+s+`"}}`))
			flusher.Flush()
		}
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
		flusher.Flush()
	})

	p := anthropicProviderFor(srv.URL)
	ch, err := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var text strings.Builder
	var done bool
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		text.WriteString(chunk.Delta)
		if chunk.Done {
			done = true
		}
	}

	if got := text.String(); got != "Hello world" {
		t.Errorf("expected %q, got %q", "Hello world", got)
	}
	if !done {
		t.Error("expected a Done chunk on message_stop")
	}
}

// A tool_use block whose arguments arrive across several input_json_delta
// events must be reassembled into one call with valid JSON.
func TestAnthropicStream_ToolUseReassembles(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprint(w, sseLine(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"file_read"}}`))
		// Arguments deliberately split mid-token across three events.
		for _, part := range []string{`{"path":"vic`, `tim.tx`, `t"}`} {
			payload, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": 1,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": part},
			})
			fmt.Fprint(w, sseLine(string(payload)))
			flusher.Flush()
		}
		fmt.Fprint(w, sseLine(`{"type":"content_block_stop","index":1}`))
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
		flusher.Flush()
	})

	p := anthropicProviderFor(srv.URL)
	ch, err := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "read it"}}, nil)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var calls []ToolCall
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		calls = append(calls, chunk.ToolCalls...)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "file_read" {
		t.Errorf("expected tool name file_read, got %q", calls[0].Name)
	}
	if calls[0].ID != "toolu_1" {
		t.Errorf("expected tool id toolu_1, got %q", calls[0].ID)
	}

	var args map[string]string
	if err := json.Unmarshal(calls[0].Args, &args); err != nil {
		t.Fatalf("reassembled args are not valid JSON: %v (raw %q)", err, calls[0].Args)
	}
	if args["path"] != "victim.txt" {
		t.Errorf("expected path victim.txt, got %q", args["path"])
	}
}

// A tool_use block that never receives arguments must still emit "{}" rather
// than an empty string, which would fail to unmarshal downstream.
func TestAnthropicStream_ToolWithNoArgsEmitsEmptyObject(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseLine(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t0","name":"code_search"}}`))
		fmt.Fprint(w, sseLine(`{"type":"content_block_stop","index":0}`))
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
		flusher.Flush()
	})

	p := anthropicProviderFor(srv.URL)
	ch, _ := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, nil)

	var calls []ToolCall
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		calls = append(calls, chunk.ToolCalls...)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if string(calls[0].Args) != "{}" {
		t.Errorf("expected empty object args, got %q", calls[0].Args)
	}
}

// An error event must surface as a chunk error rather than silently ending.
func TestAnthropicStream_ErrorEventSurfaces(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"error","delta":{"message":"overloaded"}}`))
	})

	p := anthropicProviderFor(srv.URL)
	ch, _ := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, nil)

	var gotErr error
	for chunk := range ch {
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected an error chunk for an error event")
	}
}

// Keep-alive comments and non-data lines must be ignored, not treated as JSON.
func TestAnthropicStream_IgnoresCommentsAndBlankLines(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, "event: message_start\n\n")
		fmt.Fprint(w, sseLine(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`))
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
		flusher.Flush()
	})

	p := anthropicProviderFor(srv.URL)
	ch, _ := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, nil)

	var text strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("unexpected error: %v", chunk.Err)
		}
		text.WriteString(chunk.Delta)
	}
	if got := text.String(); got != "ok" {
		t.Errorf("expected %q, got %q", "ok", got)
	}
}

// The x-api-key header and anthropic-version must be present, since the native
// Anthropic API rejects requests without them.
func TestAnthropicStream_SendsRequiredHeaders(t *testing.T) {
	var gotKey, gotVersion string
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
	})

	p := anthropicProviderFor(srv.URL)
	ch, err := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, nil)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	for range ch {
	}

	if gotKey != "test-key" {
		t.Errorf("expected x-api-key test-key, got %q", gotKey)
	}
	if gotVersion == "" {
		t.Error("expected anthropic-version header to be set")
	}
}
