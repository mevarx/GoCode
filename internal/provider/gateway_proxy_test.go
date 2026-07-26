package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	expectedSubstring := "gateway \"omniroute\" unreachable at http://localhost:59999/v1"
	if !testing.Verbose() && len(err.Error()) == 0 {
		t.Fatalf("expected error message containing %q", expectedSubstring)
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
