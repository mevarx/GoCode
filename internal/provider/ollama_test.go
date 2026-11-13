package provider

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

func TestOllamaDefaultHostResolution(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"empty defaults to local", "", "http://127.0.0.1:11434"},
		{"ip without port", "10.0.0.5", "http://10.0.0.5:11434"},
		{"hostname without port", "example.com", "http://example.com:11434"},
		{"explicit http port", "http://example.com:8080", "http://example.com:8080"},
		{"http implies port 80", "http://example.com", "http://example.com:80"},
		{"https implies port 443", "https://example.com", "https://example.com:443"},
		{"path preserved", "http://example.com:8080/prefix", "http://example.com:8080/prefix"},
		{"ipv6 with port", "[::1]:1234", "http://[::1]:1234"},
		{"bare ipv6", "::1", "http://[::1]:11434"},
		{"ipv6 without port", "http://[::1]", "http://[::1]:80"},
		{"double quoted and spaced", "  \"http://example.com:9999\"  ", "http://example.com:9999"},
		{"single quoted", "'https://example.com'", "https://example.com:443"},
		{"non numeric port falls back", "example.com:abc", "http://example.com:11434"},
		{"out of range port falls back", "example.com:70000", "http://example.com:11434"},
		{"negative port falls back", "example.com:-1", "http://example.com:11434"},
		{"empty port falls back", "example.com:", "http://example.com:11434"},
		{"ollama.com upgrades to https", "ollama.com", "https://ollama.com:443"},
		{"ollama.com with port stays http", "ollama.com:12345", "http://ollama.com:12345"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLLAMA_HOST", tc.env)
			if got := ollamaDefaultHost().String(); got != tc.want {
				t.Fatalf("OLLAMA_HOST=%q: got %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestNewOllamaProviderExplicitHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{"bare host gets http", "127.0.0.1:11434", "http://127.0.0.1:11434"},
		{"scheme preserved", "https://example.com:8443", "https://example.com:8443"},
		{"path preserved", "http://example.com/prefix", "http://example.com/prefix"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewOllamaProvider(tc.host)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := provider.base.String(); got != tc.want {
				t.Fatalf("host %q: got base %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestOllamaUseAuth(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{"unset", "", false},
		{"false", "false", false},
		{"true", "true", true},
		{"quoted false", "  \"false\" ", false},
		{"invalid non-empty means true", "yes-please", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLLAMA_AUTH", tc.env)
			if got := ollamaUseAuth(); got != tc.want {
				t.Fatalf("OLLAMA_AUTH=%q: got %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestOllamaToolCallArgs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty becomes object", "", "{}"},
		{"null becomes object", "null", "{}"},
		{"object round-trips exactly", `{"z":1,"a":2}`, `{"z":1,"a":2}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ollamaToolCallArgs(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := ollamaToolCallArgs(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("expected an error for non-object arguments")
	}
}

func TestOllamaProviderModels(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "false")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/tags" {
			t.Errorf("expected path /api/tags, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"name":"llama3:latest"},{"name":"qwen2.5-coder:7b"}]}`)
	}))
	t.Cleanup(server.Close)

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"llama3:latest", "qwen2.5-coder:7b"}
	if len(models) != len(want) {
		t.Fatalf("got %v, want %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("model %d: got %q, want %q", i, models[i], want[i])
		}
	}
}

func TestOllamaProviderSurfacesHTTPErrors(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "false")
	const serverMessage = "model requires more system memory"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":%q}`, serverMessage)
	}))
	t.Cleanup(server.Close)

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("models", func(t *testing.T) {
		_, err := provider.Models(context.Background())
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"400", serverMessage} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q must contain %q", err, want)
			}
		}
	})

	t.Run("stream", func(t *testing.T) {
		ch, err := provider.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
		chunks := drainOllamaChunks(t, ch)
		if len(chunks) == 0 {
			t.Fatal("expected an error chunk")
		}
		final := chunks[len(chunks)-1]
		if final.Err == nil {
			t.Fatal("expected an error chunk")
		}
		if !final.Done {
			t.Error("expected the error chunk to be Done")
		}
		for _, want := range []string{"400", serverMessage} {
			if !strings.Contains(final.Err.Error(), want) {
				t.Errorf("error %q must contain %q", final.Err, want)
			}
		}
	})
}

func TestOllamaProviderStreamErrorLine(t *testing.T) {
	server := newOllamaTestServer(t, `{"error":"model 'm' not found"}`+"\n")
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
		t.Fatal("expected an error chunk")
	}
	final := chunks[len(chunks)-1]
	if final.Err == nil || !strings.Contains(final.Err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %+v", final)
	}
}

func TestOllamaProviderStreamPreservesIncomingArgs(t *testing.T) {
	const args = `{"z":1,"a":{"b":[1,2]}}`
	server := newOllamaTestServer(t, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"lookup","arguments":`+args+`}}]},"done":true,"done_reason":"stop"}`+"\n")
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
	if got := string(calls[0].Args); got != args {
		t.Fatalf("args not preserved: got %q, want %q", got, args)
	}
}

// Tool schemas and history tool-call arguments must reach the wire verbatim:
// the old typed parameters struct dropped schema fields and normalized args.
func TestOllamaProviderRequestPreservesToolsAndHistoryArgs(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "false")
	got := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- body
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(server.Close)

	historyArgs := `{"z":1,"a":{"b":[1,2]}}`
	schema := `{"type":"object","properties":{"q":{"type":"string","description":"query"}},"required":["q"],"additionalProperties":false}`

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	history := []Message{
		{Role: "user", Content: "find it"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_lookup_0", Name: "lookup", Args: json.RawMessage(historyArgs)}}},
		{Role: "tool", Content: "found", ToolCallID: "call_lookup_0"},
	}
	tools := []ToolSpec{{Name: "lookup", Description: "lookup things", Parameters: json.RawMessage(schema)}}

	ch, err := provider.Stream(context.Background(), "m", history, tools)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
	for _, chunk := range drainOllamaChunks(t, ch) {
		if chunk.Err != nil {
			t.Fatalf("unexpected error chunk: %v", chunk.Err)
		}
	}

	body := <-got
	if !bytes.Contains(body, []byte(historyArgs)) {
		t.Errorf("request body lost the history arguments: %s", body)
	}
	if !bytes.Contains(body, []byte(`"additionalProperties":false`)) {
		t.Errorf("request body lost the tool schema field: %s", body)
	}
	if bytes.Contains(body, []byte(`"stream"`)) {
		t.Errorf("stream must stay omitted so Ollama defaults it to true: %s", body)
	}

	var req ollamaChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}
	if len(req.Messages[1].ToolCalls) != 1 {
		t.Fatalf("expected 1 history tool call, got %d", len(req.Messages[1].ToolCalls))
	}
	if gotArgs := string(req.Messages[1].ToolCalls[0].Function.Arguments); gotArgs != historyArgs {
		t.Errorf("history args not byte-for-byte: got %q, want %q", gotArgs, historyArgs)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Tools))
	}
	if req.Tools[0].Type != "function" || req.Tools[0].Function.Name != "lookup" || req.Tools[0].Function.Description != "lookup things" {
		t.Errorf("unexpected tool definition: %+v", req.Tools[0])
	}
	if gotParams := string(req.Tools[0].Function.Parameters); gotParams != schema {
		t.Errorf("tool parameters changed: got %q, want %q", gotParams, schema)
	}
}

// OLLAMA_AUTH=true must sign requests with ~/.ollama/id_ed25519 and carry the
// same timestamp in the challenge and the query string.
func TestOllamaProviderSignsRequestsWhenAuthEnabled(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ollama"), 0o700); err != nil {
		t.Fatalf("failed to create .ollama dir: %v", err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ollama", "id_ed25519"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("failed to write private key: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OLLAMA_AUTH", "true")

	type captured struct {
		authorization string
		ts            string
	}
	got := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- captured{authorization: r.Header.Get("Authorization"), ts: r.URL.Query().Get("ts")}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(server.Close)

	provider, err := NewOllamaProvider(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, err := provider.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
	for _, chunk := range drainOllamaChunks(t, ch) {
		if chunk.Err != nil {
			t.Fatalf("unexpected error chunk: %v", chunk.Err)
		}
	}

	request := <-got
	if request.ts == "" {
		t.Fatal("expected a ts query parameter")
	}
	parts := strings.Split(request.authorization, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("unexpected Authorization header %q", request.authorization)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[0]); err != nil {
		t.Errorf("public key is not base64: %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[1]); err != nil {
		t.Errorf("signature is not base64: %v", err)
	}
}
