package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/config"
)

// A huge non-200 body must not be buffered into the error string or logs.
func TestGatewayProxyProvider_ErrorBodyIsBounded(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		for range 256 { // 8 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	provider := NewGatewayProxyProvider("omniroute", config.GatewayConfig{BaseURL: server.URL})
	// Bypass retry backoff; the body bound is what this test measures.
	provider.client = &http.Client{Transport: http.DefaultTransport}

	const maxErrorBytes = 20 << 10

	_, err := provider.Models(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 500 models response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the status, got %q", err.Error())
	}
	if len(err.Error()) > maxErrorBytes {
		t.Errorf("models error is not bounded: %d bytes", len(err.Error()))
	}

	_, err = provider.Stream(context.Background(), "auto", []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected an error for a 500 stream response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the status, got %q", err.Error())
	}
	if len(err.Error()) > maxErrorBytes {
		t.Errorf("stream error is not bounded: %d bytes", len(err.Error()))
	}
}
