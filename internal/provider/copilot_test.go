package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/config"
)

// Points exchange and chat at one test server; returns exchange count.
func newCopilotTestProvider(t *testing.T, exchange http.HandlerFunc, chat http.HandlerFunc) (*CopilotProvider, *int32) {
	t.Helper()

	var exchangeCount int32

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCount, 1)
		// Buffer exchange so endpoints.api can be rewritten to this server.
		rec := httptest.NewRecorder()
		exchange(rec, r)
		body := strings.ReplaceAll(rec.Body.String(), "https://api.githubcopilot.com", server.URL)
		for k, vs := range rec.Header() {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("/chat/completions", chat)
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"data":[{"id":"gpt-4.1"},{"id":"gpt-5"}]}`)
	})

	p := NewCopilotProvider(config.CopilotConfig{
		OAuthTokenEnv:       "GOCODE_TEST_COPILOT_TOKEN",
		DefaultModel:        "gpt-4.1",
		EditorVersion:       "vscode/1.111.0",
		EditorPluginVersion: "copilot-chat/0.40.0",
	})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	// Allow test server plus real hosts; caching tests pass real URL but never call it.
	p.allowAPIURL = func(u *url.URL) bool {
		if u != nil && u.Host == server.Listener.Addr().String() {
			return true
		}
		return validCopilotAPIURL(u)
	}
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	return p, &exchangeCount
}

func tokenExchangeOK(apiBase string, expiresAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "copilot-jwt-value",
			"expires_at": expiresAt.Unix(),
			"endpoints":  map[string]string{"api": apiBase},
		})
	}
}

func TestCopilotTokenExchangeUsesOAuthTokenAndEditorHeaders(t *testing.T) {
	var gotAuth, gotEditor, gotPlugin, gotUA string

	exchange := func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEditor = r.Header.Get("Editor-Version")
		gotPlugin = r.Header.Get("Editor-Plugin-Version")
		gotUA = r.Header.Get("User-Agent")
		tokenExchangeOK("https://api.githubcopilot.com", time.Now().Add(30*time.Minute))(w, r)
	}

	var serverURL string
	p, _ := newCopilotTestProvider(t, exchange, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	})
	serverURL = p.tokenURL

	if _, err := p.Models(context.Background()); err != nil {
		t.Fatalf("Models should succeed against the rewritten exchange target, got %v", err)
	}
	_ = serverURL

	if gotAuth != "token ghu_test_token_value" {
		t.Errorf("expected the GitHub OAuth token in Authorization, got %q", gotAuth)
	}
	if gotEditor == "" {
		t.Error("Editor-Version header is required by Copilot")
	}
	if gotPlugin == "" {
		t.Error("Editor-Plugin-Version header is required by Copilot")
	}
	if !strings.Contains(gotUA, "GitHubCopilotChat") {
		t.Errorf("expected a GitHubCopilotChat user agent, got %q", gotUA)
	}
}

func TestCopilotCachesJWTAcrossCalls(t *testing.T) {
	p, exchanges := newCopilotTestProvider(t,
		tokenExchangeOK("https://api.githubcopilot.com", time.Now().Add(30*time.Minute)),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintln(w, `data: [DONE]`)
		})

	for i := 0; i < 3; i++ {
		if _, err := p.Models(context.Background()); err != nil {
			t.Fatalf("Models call %d failed: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(exchanges); got != 1 {
		t.Errorf("expected 1 token exchange for 3 calls (JWT should be cached), got %d", got)
	}
}

func TestCopilotRefreshesExpiringToken(t *testing.T) {
	// An expiry inside the refresh margin must not be reused.
	p, exchanges := newCopilotTestProvider(t,
		tokenExchangeOK("https://api.githubcopilot.com", time.Now().Add(10*time.Second)),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"data":[{"id":"gpt-4.1"}]}`)
		})

	for i := 0; i < 2; i++ {
		if _, err := p.Models(context.Background()); err != nil {
			t.Fatalf("Models call %d failed: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(exchanges); got != 2 {
		t.Errorf("a token inside the refresh margin must be re-exchanged, got %d exchanges", got)
	}
}

func TestCopilotReExchangesWhenOAuthTokenRotates(t *testing.T) {
	p, exchanges := newCopilotTestProvider(t,
		tokenExchangeOK("https://api.githubcopilot.com", time.Now().Add(30*time.Minute)),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"data":[{"id":"gpt-4.1"}]}`)
		})

	if _, err := p.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_rotated_token_value")
	if _, err := p.Models(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(exchanges); got != 2 {
		t.Errorf("a rotated OAuth token must trigger a fresh exchange, got %d exchanges", got)
	}
}

func TestCopilotStreamSendsBearerJWTToDynamicBaseURL(t *testing.T) {
	var gotAuth string
	var chatServed bool

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "copilot-jwt-value",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": server.URL},
		})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		chatServed = true
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"hello"}}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	})

	p := NewCopilotProvider(config.CopilotConfig{
		OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN",
		DefaultModel:  "gpt-4.1",
	})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	p.allowAPIURL = func(u *url.URL) bool { return u != nil && u.Host == server.Listener.Addr().String() }
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	ch, err := p.Stream(context.Background(), "gpt-4.1", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	var text string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		text += chunk.Delta
	}

	if !chatServed {
		t.Fatal("expected the request to reach the base URL returned by the exchange")
	}
	if gotAuth != "Bearer copilot-jwt-value" {
		t.Errorf("expected the exchanged JWT as a bearer token, got %q", gotAuth)
	}
	if text != "hello" {
		t.Errorf("expected stream text 'hello', got %q", text)
	}
}

// Redirect all platform config paths to temp dir; only APPDATA leaked to ~/.config on macOS/Linux.
func isolateConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
}

func TestCopilotErrorsWhenNoOAuthToken(t *testing.T) {
	isolateConfigDir(t)
	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_MISSING_TOKEN"})
	t.Setenv("GOCODE_TEST_MISSING_TOKEN", "")

	_, err := p.Models(context.Background())
	if err == nil {
		t.Fatal("expected an error when no GitHub token is configured")
	}
	if !strings.Contains(err.Error(), "gocode auth copilot") {
		t.Errorf("error should tell the user how to log in, got %q", err)
	}
}

// HasToken must see the saved file, not just env.
func TestCopilotHasTokenSeesSavedTokenFile(t *testing.T) {
	envName := "GOCODE_TEST_COPILOT_TOKEN"
	t.Setenv(envName, "")

	isolateConfigDir(t)

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: envName})
	if p.HasToken() {
		t.Fatal("HasToken should be false with no env var and no saved file")
	}

	path := config.CopilotTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("  ghu_saved_token_value\n"), 0o600); err != nil {
		t.Fatalf("failed to write token: %v", err)
	}

	if !p.HasToken() {
		t.Error("HasToken must see a token saved by `gocode auth copilot`")
	}
	// Saved token must be trimmed.
	if got := p.oauthToken(); got != "ghu_saved_token_value" {
		t.Errorf("saved token not trimmed: got %q", got)
	}
}

// Env wins over saved file so stale login can be overridden without device flow.
func TestCopilotEnvTokenOverridesSavedFile(t *testing.T) {
	envName := "GOCODE_TEST_COPILOT_TOKEN"
	isolateConfigDir(t)

	path := config.CopilotTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ghu_from_file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envName, "ghu_from_env")

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: envName})
	if got := p.oauthToken(); got != "ghu_from_env" {
		t.Errorf("env var must win over the saved file, got %q", got)
	}
	if got := p.TokenEnvName(); got != envName {
		t.Errorf("TokenEnvName = %q, want %q", got, envName)
	}
}

// Exchange base URL is untrusted; JWT to attacker host would leak credential, must reject.
func TestCopilotRejectsUntrustedAPIBaseURL(t *testing.T) {
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the token must never be sent to an untrusted host")
	}))
	t.Cleanup(attacker.Close)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "copilot-jwt-value",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": attacker.URL},
		})
	})

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN"})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	if _, err := p.Models(context.Background()); err == nil {
		t.Fatal("expected an untrusted API host to be rejected")
	}
}

func TestCopilotRejectsPlaintextAPIBaseURL(t *testing.T) {
	if _, err := resolveCopilotAPI("http://api.githubcopilot.com", validCopilotAPIURL); err == nil {
		t.Error("a non-HTTPS base URL must be rejected")
	}
}

func TestValidCopilotAPIURL(t *testing.T) {
	mustParse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("bad test URL %q: %v", raw, err)
		}
		return u
	}

	trusted := []string{
		"https://api.githubcopilot.com",
		"https://prod.api.githubcopilot.com",
		"https://api.github.com",
		"https://API.GITHUBCOPILOT.COM",
	}
	for _, raw := range trusted {
		if !validCopilotAPIURL(mustParse(raw)) {
			t.Errorf("expected %q to be trusted", raw)
		}
	}

	// Each defeats a naive check: non-GitHub host, non-HTTPS, or suffix trick.
	untrusted := []string{
		"https://evil.com",
		"https://api.githubcopilot.com.evil.com",
		"https://githubcopilot.com.evil.com",
		"https://notgithub.com",
		"http://api.githubcopilot.com",
		"https://api.githubcopilot.com:8443",
		"https://127.0.0.1",
		"https://localhost",
		"https://user:pass@api.githubcopilot.com",
		// Query/fragment would swallow the appended /chat/completions path.
		"https://api.githubcopilot.com?x=1",
		"https://api.githubcopilot.com#frag",
	}
	for _, raw := range untrusted {
		if validCopilotAPIURL(mustParse(raw)) {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
}

func TestResolveCopilotAPIFallsBackWhenAbsent(t *testing.T) {
	got, err := resolveCopilotAPI("", validCopilotAPIURL)
	if err != nil {
		t.Fatalf("an absent base URL should fall back, got %v", err)
	}
	if got != copilotFallbackAPI {
		t.Errorf("expected fallback %q, got %q", copilotFallbackAPI, got)
	}
}

func TestCopilotModelsFailureSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/copilot_internal/v2/token", tokenExchangeOK(server.URL, time.Now().Add(30*time.Minute)))
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, `{"error":"upstream down"}`)
	})

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN"})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	p.allowAPIURL = func(u *url.URL) bool { return u != nil && u.Host == server.Listener.Addr().String() }
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	if _, err := p.Models(context.Background()); err == nil {
		t.Fatal("Models must surface a failure instead of returning a canned list")
	}
}

// 401 must invalidate cached JWT and recover with one re-exchange + retry.
func TestCopilotStream401InvalidatesCachedToken(t *testing.T) {
	var chatCalls int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	var exchangeCount int32
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchangeCount, 1)
		tokenExchangeOK(server.URL, time.Now().Add(30*time.Minute))(w, r)
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&chatCalls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintln(w, `{"error":"token expired"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	})

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN"})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	p.allowAPIURL = func(u *url.URL) bool { return u != nil && u.Host == server.Listener.Addr().String() }
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	ch, err := p.Stream(context.Background(), "gpt-4.1", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	var text string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		text += chunk.Delta
	}
	if text != "ok" {
		t.Errorf("expected the retried stream, got %q", text)
	}
	if got := atomic.LoadInt32(&exchangeCount); got != 2 {
		t.Errorf("expected 2 token exchanges after the 401, got %d", got)
	}
	if got := atomic.LoadInt32(&chatCalls); got != 2 {
		t.Errorf("expected the chat request to be retried once, got %d", got)
	}
}

func TestCopilotSurfacesNoSubscriptionError(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintln(w, `{"message":"no active subscription"}`)
	})

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN"})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	_, _, err := p.token(context.Background())
	if err == nil {
		t.Fatal("expected an error when the exchange is rejected")
	}
	if !strings.Contains(err.Error(), "Copilot subscription") {
		t.Errorf("error should mention the subscription requirement, got %q", err)
	}
}

func TestCopilotRejectsEmptyToken(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"token":"","expires_at":1234567890}`)
	})

	p := NewCopilotProvider(config.CopilotConfig{OAuthTokenEnv: "GOCODE_TEST_COPILOT_TOKEN"})
	p.client = server.Client()
	p.tokenURL = server.URL + "/copilot_internal/v2/token"
	t.Setenv("GOCODE_TEST_COPILOT_TOKEN", "ghu_test_token_value")

	if _, _, err := p.token(context.Background()); err == nil {
		t.Fatal("an empty token must be an error, not a usable credential")
	}
}

// Reasoning streams separately from answer; parser must preserve it.
func TestStreamOpenAISSEPreservesReasoning(t *testing.T) {
	body := strings.Join([]string{
		`: keepalive`,
		`data: {"choices":[{"delta":{"reasoning_content":"thinking..."}}]}`,
		`data: {"choices":[{"delta":{"content":"answer"}}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	ch := make(chan StreamChunk, 16)
	go func() {
		defer close(ch)
		streamOpenAISSE(context.Background(), strings.NewReader(body), ch, "test")
	}()

	var text, reasoning string
	done := false
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		text += chunk.Delta
		reasoning += chunk.Reasoning
		if chunk.Done {
			done = true
		}
	}

	if reasoning != "thinking..." {
		t.Errorf("expected reasoning 'thinking...', got %q", reasoning)
	}
	if text != "answer" {
		t.Errorf("expected content 'answer', got %q", text)
	}
	if !done {
		t.Error("expected a Done chunk")
	}
}

// Assistant reasoning must be replayed; dropping it breaks multi-turn continuity.
func TestBuildOpenAIMessagesReplaysReasoning(t *testing.T) {
	msgs := buildOpenAIMessages([]Message{
		{Role: "assistant", Content: "text", ReasoningContent: "chain"},
		{Role: "user", Content: "next"},
	})

	if msgs[0].ReasoningContent != "chain" {
		t.Errorf("assistant reasoning must be replayed, got %q", msgs[0].ReasoningContent)
	}
	if msgs[1].ReasoningContent != "" {
		t.Errorf("user messages carry no reasoning, got %q", msgs[1].ReasoningContent)
	}

	encoded, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "reasoning_content") {
		t.Errorf("reasoning_content must be on the wire, got %s", encoded)
	}
}
