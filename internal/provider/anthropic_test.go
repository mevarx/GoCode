package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

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

// Missing args must emit "{}", not "", which fails to unmarshal downstream.
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

// Real error shape {"type":"error","error":{...}}; message must survive to caller.
func TestAnthropicStream_ErrorEventRealPayload(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
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
		t.Fatal("expected an error chunk")
	}
	if !strings.Contains(gotErr.Error(), "Overloaded") {
		t.Errorf("expected the real error message, got %q", gotErr.Error())
	}
}

// Empty turns must be dropped or the API 400s with "content":[].
func TestAnthropicStream_DropsEmptyTurnsFromRequest(t *testing.T) {
	var body []byte
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
	})

	p := anthropicProviderFor(srv.URL)
	history := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: ""},
		{Role: "assistant", Content: "  "},
		{Role: "tool", Content: "", ToolCallID: "t0"},
		{Role: "user", Content: "next"},
	}
	ch, err := p.Stream(context.Background(), "claude-test", history, nil)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	for range ch {
	}

	s := string(body)
	if strings.Contains(s, `"content":[]`) {
		t.Errorf("empty assistant turn serialised as content:[]: %s", s)
	}
	if strings.Contains(s, `"tool_result"`) {
		t.Errorf("empty tool_result block was forwarded: %s", s)
	}
	if !strings.Contains(s, `"content":"hi"`) || !strings.Contains(s, `"content":"next"`) {
		t.Errorf("non-empty messages must survive: %s", s)
	}
}

// Tool args over bufio's 64 KiB cap must not abort the turn.
func TestAnthropicStream_LargeToolArgumentScannerCap(t *testing.T) {
	big := strings.Repeat("x", 100*1024)
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t0","name":"file_write"}}`))
		payload, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"content":"` + big + `"}`},
		})
		fmt.Fprint(w, sseLine(string(payload)))
		fmt.Fprint(w, sseLine(`{"type":"content_block_stop","index":0}`))
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
	})

	p := anthropicProviderFor(srv.URL)
	ch, _ := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, nil)

	var calls []ToolCall
	var gotErr error
	for chunk := range ch {
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
		calls = append(calls, chunk.ToolCalls...)
	}
	if gotErr != nil {
		t.Fatalf("large tool argument must not abort the stream: %v", gotErr)
	}
	if len(calls) != 1 || len(calls[0].Args) < 100*1024 {
		t.Errorf("expected the full large argument, got %d calls, args len %d", len(calls), lenOfArgs(calls))
	}
}

func lenOfArgs(calls []ToolCall) int {
	if len(calls) == 0 {
		return 0
	}
	return len(calls[0].Args)
}

// stop_reason max_tokens must surface, not store truncated answer as complete.
func TestAnthropicStream_MaxTokensStopReasonTruncates(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`))
		fmt.Fprint(w, sseLine(`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`))
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
	})

	p := anthropicProviderFor(srv.URL)
	ch, _ := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, nil)

	var gotErr error
	var gotFinish string
	for chunk := range ch {
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
		if chunk.FinishReason != "" {
			gotFinish = chunk.FinishReason
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "max_tokens") {
		t.Errorf("expected truncation error mentioning max_tokens, got %v", gotErr)
	}
	if gotFinish != "max_tokens" {
		t.Errorf("expected FinishReason max_tokens, got %q", gotFinish)
	}
}

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

// Nil schema must default, not forward "input_schema":null which fails the request.
func TestAnthropicStream_NullToolSchemaDefaulted(t *testing.T) {
	var body []byte
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
	})

	p := anthropicProviderFor(srv.URL)
	ch, _ := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "go"}}, []ToolSpec{{Name: "t", Description: "d", Parameters: nil}})
	for range ch {
	}
	if strings.Contains(string(body), `"input_schema":null`) {
		t.Errorf("null input_schema was forwarded: %s", body)
	}
	if !strings.Contains(string(body), `"input_schema":{"type":"object"`) {
		t.Errorf("expected defaulted input_schema, got %s", body)
	}
}

func TestMaxTokensForModel(t *testing.T) {
	if got := maxTokensForModel("claude-3-opus-20240229"); got != 4096 {
		t.Errorf("claude-3-opus must request at most 4096, got %d", got)
	}
	if got := maxTokensForModel("claude-sonnet-4-20250514"); got != 8192 {
		t.Errorf("other models keep the 8192 default, got %d", got)
	}
}

// Native API rejects requests without x-api-key and anthropic-version.
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
