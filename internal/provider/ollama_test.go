package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newOllamaTestServer serves a scripted NDJSON body on /api/chat.
func newOllamaTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	// Keep upstream auth signing out of tests regardless of the developer's environment.
	t.Setenv("OLLAMA_AUTH", "false")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected path /api/chat, got %s", r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func drainOllamaChunks(t *testing.T, ch <-chan StreamChunk) []StreamChunk {
	t.Helper()
	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// Ollama signals token-budget truncation with done_reason "length"; it must reach
// StreamChunk.FinishReason so the agent loop can warn instead of silently cutting.
func TestOllamaProviderStreamFinishReasonLength(t *testing.T) {
	server := newOllamaTestServer(t, `{"model":"m","message":{"role":"assistant","content":"partial"},"done":false}`+"\n"+
		`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"length"}`+"\n")

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, err := provider.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	chunks := drainOllamaChunks(t, ch)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d: %+v", len(chunks), chunks)
	}

	text := ""
	for _, chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("truncation must not surface as an error, got %v", chunk.Err)
		}
		text += chunk.Delta
	}
	if text != "partial" {
		t.Errorf("expected content %q, got %q", "partial", text)
	}

	final := chunks[len(chunks)-1]
	if !final.Done {
		t.Errorf("expected final chunk to be Done")
	}
	if final.FinishReason != "length" {
		t.Errorf("expected FinishReason %q, got %q", "length", final.FinishReason)
	}
}

func TestOllamaProviderStreamFinishReasonStop(t *testing.T) {
	server := newOllamaTestServer(t, `{"model":"m","message":{"role":"assistant","content":"hello"},"done":false}`+"\n"+
		`{"model":"m","message":{"role":"assistant","content":" world"},"done":true,"done_reason":"stop"}`+"\n")

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, err := provider.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	chunks := drainOllamaChunks(t, ch)
	if len(chunks) == 0 {
		t.Fatal("expected chunks, got none")
	}

	final := chunks[len(chunks)-1]
	if final.Err != nil {
		t.Fatalf("unexpected error chunk: %v", final.Err)
	}
	if !final.Done {
		t.Errorf("expected final chunk to be Done")
	}
	if final.FinishReason != "stop" {
		t.Errorf("expected FinishReason %q, got %q", "stop", final.FinishReason)
	}
}

// Tool-call marshaling must stay intact while DoneReason handling is added.
func TestOllamaProviderStreamToolCalls(t *testing.T) {
	server := newOllamaTestServer(t, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.go"}}}]},"done":false}`+"\n"+
		`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, err := provider.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	var calls []ToolCall
	for _, chunk := range drainOllamaChunks(t, ch) {
		if chunk.Err != nil {
			t.Fatalf("unexpected error chunk: %v", chunk.Err)
		}
		calls = append(calls, chunk.ToolCalls...)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Name != "read_file" {
		t.Errorf("expected tool call name %q, got %q", "read_file", calls[0].Name)
	}
	if string(calls[0].Args) != `{"path":"a.go"}` {
		t.Errorf("unexpected tool call args: %s", calls[0].Args)
	}
}

// An empty host must keep resolving through OLLAMA_HOST, but with our bounded client.
func TestOllamaProviderResolvesHostFromEnvironment(t *testing.T) {
	server := newOllamaTestServer(t, `{"model":"m","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`+"\n")
	t.Setenv("OLLAMA_HOST", server.URL)

	provider, err := NewOllamaProvider("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, err := provider.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	chunks := drainOllamaChunks(t, ch)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from OLLAMA_HOST server, got none")
	}
	if chunks[len(chunks)-1].Err != nil {
		t.Fatalf("unexpected error chunk: %v", chunks[len(chunks)-1].Err)
	}
}

// The Ollama HTTP client must never be http.DefaultClient: it needs a retry
// transport and a bounded timeout so a stalled server cannot hang a turn.
func TestNewOllamaHTTPClientIsBounded(t *testing.T) {
	client := newOllamaHTTPClient()
	if client == http.DefaultClient {
		t.Fatal("ollama client must not be http.DefaultClient")
	}
	if client.Timeout != ollamaRequestTimeout {
		t.Errorf("expected timeout %v, got %v", ollamaRequestTimeout, client.Timeout)
	}
	if _, ok := client.Transport.(*RetryTransport); !ok {
		t.Errorf("expected RetryTransport, got %T", client.Transport)
	}
}
