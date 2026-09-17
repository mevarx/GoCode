package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

func TestGatewayProxyProvider_Models(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("expected path /models, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Fatalf("expected auth header Bearer secret-key, got %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"data": [{"id": "auto"}, {"id": "auto/coding"}, {"id": "claude-sonnet-4.5"}]}`)
	}))
	defer server.Close()

	cfg := config.GatewayConfig{
		BaseURL:      server.URL,
		APIKey:       "secret-key",
		DefaultModel: "auto",
	}

	provider := NewGatewayProxyProvider("omniroute", cfg)
	if provider.Name() != "omniroute" {
		t.Fatalf("expected name omniroute, got %s", provider.Name())
	}

	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	if models[0] != "auto" || models[1] != "auto/coding" || models[2] != "claude-sonnet-4.5" {
		t.Fatalf("unexpected models list: %v", models)
	}
}

func TestGatewayProxyProvider_UnreachableError(t *testing.T) {
	cfg := config.GatewayConfig{
		BaseURL:      "http://localhost:59999/v1",
		APIKey:       "",
		DefaultModel: "auto",
	}

	provider := NewGatewayProxyProvider("omniroute", cfg)
	_, err := provider.Models(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable gateway, got nil")
	}

	expectedSubstring := `provider "omniroute" unreachable at http://localhost:59999/v1`
	if !strings.Contains(err.Error(), expectedSubstring) {
		t.Fatalf("expected error containing %q, got %q", expectedSubstring, err.Error())
	}
}

func TestGatewayProxyProvider_Stream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("expected path /chat/completions, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices": [{"delta": {"content": "Hello"}}]} `)
		fmt.Fprintln(w, `data: {"choices": [{"delta": {"content": " world"}}]} `)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer server.Close()

	cfg := config.GatewayConfig{
		BaseURL:      server.URL,
		APIKey:       "",
		DefaultModel: "auto",
	}

	provider := NewGatewayProxyProvider("omniroute", cfg)
	ch, err := provider.Stream(context.Background(), "auto", []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var text string
	done := false
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		text += chunk.Delta
		if chunk.Done {
			done = true
		}
	}

	if !done {
		t.Fatal("expected stream done flag to be true")
	}
	if text != "Hello world" {
		t.Fatalf("expected 'Hello world', got %q", text)
	}
}

func TestGatewayProxyProvider_StreamMultipleToolCallsOrdered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Send fragmented tool calls (deliberately out of order: index 1 first, then index 0)
		fmt.Fprintln(w, `data: {"choices": [{"delta": {"tool_calls": [{"index": 1, "id": "call_b", "function": {"name": "tool_b", "arguments": "{\"b\":"}}]}}]} `)
		fmt.Fprintln(w, `data: {"choices": [{"delta": {"tool_calls": [{"index": 0, "id": "call_a", "function": {"name": "tool_a", "arguments": "{\"a\":"}}]}}]} `)
		fmt.Fprintln(w, `data: {"choices": [{"delta": {"tool_calls": [{"index": 1, "function": {"arguments": "2}"}}]}}]} `)
		fmt.Fprintln(w, `data: {"choices": [{"delta": {"tool_calls": [{"index": 0, "function": {"arguments": "1}"}}]}}]} `)
		fmt.Fprintln(w, `data: {"choices": [{"finish_reason": "tool_calls"}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer server.Close()

	cfg := config.GatewayConfig{
		BaseURL:      server.URL,
		APIKey:       "",
		DefaultModel: "auto",
	}

	provider := NewGatewayProxyProvider("testprov", cfg)
	ch, err := provider.Stream(context.Background(), "auto", []Message{
		{Role: "user", Content: "call tools"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var assembledTools []ToolCall
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if len(chunk.ToolCalls) > 0 {
			assembledTools = append(assembledTools, chunk.ToolCalls...)
		}
	}

	if len(assembledTools) != 2 {
		t.Fatalf("expected 2 assembled tool calls, got %d", len(assembledTools))
	}

	// Must be ordered by index: index 0 first, index 1 second
	if assembledTools[0].ID != "call_a" || assembledTools[0].Name != "tool_a" {
		t.Errorf("expected tool 0 to be call_a, got %+v", assembledTools[0])
	}
	if string(assembledTools[0].Args) != `{"a":1}` {
		t.Errorf("expected args '{\"a\":1}', got %s", string(assembledTools[0].Args))
	}

	if assembledTools[1].ID != "call_b" || assembledTools[1].Name != "tool_b" {
		t.Errorf("expected tool 1 to be call_b, got %+v", assembledTools[1])
	}
	if string(assembledTools[1].Args) != `{"b":2}` {
		t.Errorf("expected args '{\"b\":2}', got %s", string(assembledTools[1].Args))
	}
}
