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

// drainChunks collects a whole stream and returns every reported usage, in
// order. A usage report is optional, so an empty result is not an error.
func drainChunks(t *testing.T, ch <-chan StreamChunk) (text string, usages []Usage, done bool) {
	t.Helper()
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		text += chunk.Delta
		if chunk.Usage != nil {
			usages = append(usages, *chunk.Usage)
		}
		if chunk.Done {
			done = true
		}
	}
	return text, usages, done
}

// OpenAI-compatible final-chunk usage is the format 17 gateways share.
func TestStreamOpenAISSEParsesFinalChunkUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	ch := make(chan StreamChunk, 16)
	go func() {
		defer close(ch)
		streamOpenAISSE(context.Background(), strings.NewReader(body), ch, "test")
	}()

	text, usages, done := drainChunks(t, ch)
	if text != "hi" {
		t.Errorf("delta text = %q, want %q", text, "hi")
	}
	if !done {
		t.Error("stream never reported done")
	}
	if len(usages) != 1 {
		t.Fatalf("expected exactly one usage report, got %d: %+v", len(usages), usages)
	}
	if usages[0].PromptTokens != 123 || usages[0].CompletionTokens != 45 {
		t.Errorf("usage = %+v, want prompt 123 / completion 45", usages[0])
	}
}

// Usage can share a chunk with a choice, and a stream without usage must not
// fail or invent one.
func TestStreamOpenAISSEUsageIsOptional(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"a"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`data: {"choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	ch := make(chan StreamChunk, 16)
	go func() {
		defer close(ch)
		streamOpenAISSE(context.Background(), strings.NewReader(body), ch, "test")
	}()

	text, usages, done := drainChunks(t, ch)
	if text != "ab" {
		t.Errorf("delta text = %q, want %q", text, "ab")
	}
	if !done {
		t.Error("stream never reported done")
	}
	if len(usages) != 1 || usages[0].PromptTokens != 10 || usages[0].CompletionTokens != 2 {
		t.Errorf("usage = %+v, want the in-chunk report 10/2", usages)
	}
}

// Anthropic reports the prompt count once (message_start) and the completion
// count cumulatively (message_delta); both must land on one usage report.
func TestAnthropicStreamReportsUsage(t *testing.T) {
	srv := newAnthropicTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLine(`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":100,"output_tokens":1}}}`))
		fmt.Fprint(w, sseLine(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`))
		fmt.Fprint(w, sseLine(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`))
		fmt.Fprint(w, sseLine(`{"type":"message_stop"}`))
	})

	p := anthropicProviderFor(srv.URL)
	ch, err := p.Stream(context.Background(), "claude-test", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	text, usages, done := drainChunks(t, ch)
	if text != "hello" {
		t.Errorf("delta text = %q, want %q", text, "hello")
	}
	if !done {
		t.Error("stream never reported done")
	}
	if len(usages) == 0 {
		t.Fatal("no usage reported for a stream that carried message_start and message_delta usage")
	}
	last := usages[len(usages)-1]
	if last.PromptTokens != 100 || last.CompletionTokens != 42 {
		t.Errorf("final usage = %+v, want prompt 100 / completion 42 (cumulative)", last)
	}
	// Reports are cumulative, never additive: no report may exceed the final
	// count, or a consumer that sums reports would double-count.
	for _, u := range usages {
		if u.CompletionTokens > 42 || u.PromptTokens > 100 {
			t.Errorf("cumulative usage went backwards: %+v (final %+v)", u, last)
		}
	}
}

// Ollama reports prompt_eval_count/eval_count on the done response.
func TestOllamaStreamReportsUsage(t *testing.T) {
	server := newOllamaTestServer(t,
		`{"message":{"role":"assistant","content":"hi"},"done":false}`+"\n"+
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":77,"eval_count":21}`+"\n")

	p, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	ch, err := p.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	text, usages, done := drainChunks(t, ch)
	if text != "hi" {
		t.Errorf("delta text = %q, want %q", text, "hi")
	}
	if !done {
		t.Error("stream never reported done")
	}
	if len(usages) != 1 {
		t.Fatalf("expected one usage report, got %d: %+v", len(usages), usages)
	}
	if usages[0].PromptTokens != 77 || usages[0].CompletionTokens != 21 {
		t.Errorf("usage = %+v, want prompt 77 / completion 21", usages[0])
	}
}

// The request opt-in belongs to the config switch: absent by default, present
// as stream_options.include_usage when enabled.
func TestGatewayStreamUsageRequestOption(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{name: "default sends nothing", enabled: false, want: false},
		{name: "opt-in sends include_usage", enabled: true, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bodies := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("request body is not JSON: %v", err)
				}
				bodies <- body
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintln(w, `data: [DONE]`)
			}))
			defer server.Close()

			p := NewGatewayProxyProvider("gw", config.GatewayConfig{BaseURL: server.URL}).
				WithStreamUsage(tc.enabled)
			ch, err := p.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
			if err != nil {
				t.Fatalf("stream error: %v", err)
			}
			for range ch {
			}

			body := <-bodies
			opts, present := body["stream_options"]
			if !tc.want {
				if present {
					t.Errorf("stream_options sent without opt-in: %#v", opts)
				}
				return
			}
			obj, ok := opts.(map[string]any)
			if !ok {
				t.Fatalf("stream_options = %#v, want an object", opts)
			}
			if obj["include_usage"] != true {
				t.Errorf("include_usage = %#v, want true", obj["include_usage"])
			}
		})
	}
}
