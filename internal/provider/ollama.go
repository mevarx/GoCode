package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ollamaRequestTimeout bounds a whole chat turn, including streamed body reads,
// so a stalled Ollama server cannot hold a turn forever.
const ollamaRequestTimeout = 10 * time.Minute

// ollamaStreamBuffer caps a single NDJSON response line; the 64 KiB default
// truncates long tool arguments.
const ollamaStreamBuffer = 8 * 1024 * 1024

type OllamaProvider struct {
	base   *url.URL
	client *http.Client
}

// newOllamaHTTPClient builds the HTTP client used for Ollama traffic. It never
// uses http.DefaultClient, which has no timeout and no retry policy.
func newOllamaHTTPClient() *http.Client {
	baseTransport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{
		Transport: NewRetryTransport(baseTransport),
		Timeout:   ollamaRequestTimeout,
	}
}

// ollamaVar mirrors the upstream Var helper: surrounding whitespace trimmed
// first, then surrounding double or single quotes stripped.
func ollamaVar(key string) string {
	return strings.Trim(strings.TrimSpace(os.Getenv(key)), "\"'")
}

// ollamaDefaultHost resolves OLLAMA_HOST with the same quirks as the upstream
// host resolver: missing scheme means http (https for bare ollama.com),
// http/https imply default ports 80/443, anything after the first "/" becomes
// the URL path, and unparsable hosts fall back to 127.0.0.1.
func ollamaDefaultHost() *url.URL {
	defaultPort := "11434"

	s := ollamaVar("OLLAMA_HOST")
	scheme, hostport, ok := strings.Cut(s, "://")
	switch {
	case !ok:
		scheme, hostport = "http", s
		if s == "ollama.com" {
			scheme, hostport = "https", "ollama.com:443"
		}
	case scheme == "http":
		defaultPort = "80"
	case scheme == "https":
		defaultPort = "443"
	}

	hostport, path, _ := strings.Cut(hostport, "/")
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = "127.0.0.1", defaultPort
		if ip := net.ParseIP(strings.Trim(hostport, "[]")); ip != nil {
			host = ip.String()
		} else if hostport != "" {
			host = hostport
		}
	}

	if n, err := strconv.ParseInt(port, 10, 32); err != nil || n > 65535 || n < 0 {
		port = defaultPort
	}

	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, port),
		Path:   path,
	}
}

func NewOllamaProvider(host string) (*OllamaProvider, error) {
	var base *url.URL
	if host != "" {
		hostURL := host
		if !strings.Contains(hostURL, "://") {
			hostURL = "http://" + hostURL
		}
		u, err := url.Parse(hostURL)
		if err != nil {
			return nil, fmt.Errorf("invalid ollama host %q: %w", host, err)
		}
		base = u
	} else {
		// Same OLLAMA_HOST resolution the upstream client performed, but with our
		// own bounded client instead of http.DefaultClient.
		base = ollamaDefaultHost()
	}

	return &OllamaProvider{
		base:   base,
		client: newOllamaHTTPClient(),
	}, nil
}

func (o *OllamaProvider) Name() string {
	return "ollama"
}

// ollamaUseAuth mirrors the upstream OLLAMA_AUTH switch: unset/empty is false,
// invalid non-empty values are treated as true.
func ollamaUseAuth() bool {
	s := ollamaVar("OLLAMA_AUTH")
	if s == "" {
		return false
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return true
	}
	return v
}

// ollamaAuthToken signs the challenge with ~/.ollama/id_ed25519 and returns
// "<base64 pubkey without type>:<base64 signature>", as upstream auth.Sign does.
func ollamaAuthToken(challenge string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	keyPath := filepath.Join(home, ".ollama", "id_ed25519")
	privateKeyFile, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}

	privateKey, err := ssh.ParsePrivateKey(privateKeyFile)
	if err != nil {
		return "", err
	}

	publicKey := ssh.MarshalAuthorizedKey(privateKey.PublicKey())
	parts := bytes.Split(publicKey, []byte(" "))
	if len(parts) < 2 {
		return "", errors.New("malformed ollama public key")
	}

	signedData, err := privateKey.Sign(rand.Reader, []byte(challenge))
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s:%s", bytes.TrimSpace(parts[1]), base64.StdEncoding.EncodeToString(signedData.Blob)), nil
}

// newRequest builds a request against the configured base URL, signing it when
// OLLAMA_AUTH is enabled or the host is ollama.com.
func (o *OllamaProvider) newRequest(ctx context.Context, method, path string, body []byte, accept string) (*http.Request, error) {
	requestURL := o.base.JoinPath(path)

	var token string
	if ollamaUseAuth() || o.base.Hostname() == "ollama.com" {
		now := strconv.FormatInt(time.Now().Unix(), 10)
		signed, err := ollamaAuthToken(fmt.Sprintf("%s,%s?ts=%s", method, path, now))
		if err != nil {
			return nil, err
		}
		token = signed

		q := requestURL.Query()
		q.Set("ts", now)
		requestURL.RawQuery = q.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return req, nil
}

// ollamaStatusError describes a non-2xx response, preferring the server's
// {"error": "..."} message when present.
func ollamaStatusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var payload struct {
		Error string `json:"error"`
	}
	message := ""
	if err := json.Unmarshal(body, &payload); err == nil {
		message = payload.Error
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		return fmt.Errorf("ollama returned status %s", resp.Status)
	}
	return fmt.Errorf("ollama returned status %s: %s", resp.Status, message)
}

func (o *OllamaProvider) Models(ctx context.Context) ([]string, error) {
	req, err := o.newRequest(ctx, http.MethodGet, "/api/tags", nil, "application/json")
	if err != nil {
		return nil, fmt.Errorf("failed to list ollama models: %w", err)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list ollama models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("failed to list ollama models: %w", ollamaStatusError(resp))
	}

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to list ollama models: %w", err)
	}

	models := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		models = append(models, m.Name)
	}
	return models, nil
}

type ollamaToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Function ollamaToolCallFunction `json:"function"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
}

type ollamaChatResponse struct {
	Message    ollamaResponseMessage `json:"message"`
	Done       bool                  `json:"done"`
	DoneReason string                `json:"done_reason,omitempty"`
}

type ollamaResponseMessage struct {
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls"`
}

// ollamaToolCallArgs returns raw arguments untouched so history round-trips
// byte-for-byte; empty/null arguments become an empty object.
func ollamaToolCallArgs(raw json.RawMessage) (json.RawMessage, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid([]byte(s)) || !strings.HasPrefix(s, "{") {
		return nil, fmt.Errorf("invalid tool call arguments: %s", s)
	}
	return json.RawMessage(s), nil
}

func ollamaMessages(history []Message) ([]ollamaMessage, error) {
	messages := make([]ollamaMessage, 0, len(history))
	for _, msg := range history {
		om := ollamaMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
		for _, tc := range msg.ToolCalls {
			args, err := ollamaToolCallArgs(tc.Args)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool call args: %w", err)
			}
			om.ToolCalls = append(om.ToolCalls, ollamaToolCall{
				Function: ollamaToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		messages = append(messages, om)
	}
	return messages, nil
}

func ollamaTools(tools []ToolSpec) []ollamaTool {
	if len(tools) == 0 {
		return nil
	}
	specs := make([]ollamaTool, 0, len(tools))
	for _, ts := range tools {
		specs = append(specs, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        ts.Name,
				Description: ts.Description,
				// Raw schema keeps additionalProperties, descriptions and other
				// fields the upstream parameters struct dropped.
				Parameters: json.RawMessage(validOrDefaultToolSchema(ts.Parameters)),
			},
		})
	}
	return specs
}

func (o *OllamaProvider) Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error) {
	messages, err := ollamaMessages(history)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(ollamaChatRequest{
		Model:    model,
		Messages: messages,
		Tools:    ollamaTools(tools),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ollama chat request: %w", err)
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)

		req, err := o.newRequest(ctx, http.MethodPost, "/api/chat", payload, "application/x-ndjson")
		if err != nil {
			emitChunk(ctx, ch, StreamChunk{Err: err, Done: true})
			return
		}

		resp, err := o.client.Do(req)
		if err != nil {
			emitChunk(ctx, ch, StreamChunk{Err: err, Done: true})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			emitChunk(ctx, ch, StreamChunk{Err: ollamaStatusError(resp), Done: true})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), ollamaStreamBuffer)
		for scanner.Scan() {
			line := scanner.Bytes()

			var errPayload struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(line, &errPayload); err != nil {
				emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("failed to decode ollama response: %w", err), Done: true})
				return
			}
			if errPayload.Error != "" {
				emitChunk(ctx, ch, StreamChunk{Err: errors.New(errPayload.Error), Done: true})
				return
			}

			var resp ollamaChatResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("failed to decode ollama response: %w", err), Done: true})
				return
			}

			chunk := StreamChunk{
				Delta: resp.Message.Content,
				Done:  resp.Done,
			}
			if resp.Done {
				// Ollama reports "length" when the token budget cut generation short and
				// "stop"/"load" otherwise, so surface it rather than losing the truncation.
				chunk.FinishReason = resp.DoneReason
			}

			for i, tc := range resp.Message.ToolCalls {
				args, err := ollamaToolCallArgs(tc.Function.Arguments)
				if err != nil {
					emitChunk(ctx, ch, StreamChunk{Err: fmt.Errorf("failed to marshal tool call args: %w", err), Done: true})
					return
				}
				chunk.ToolCalls = append(chunk.ToolCalls, ToolCall{
					ID:   fmt.Sprintf("call_%s_%d", tc.Function.Name, i),
					Name: tc.Function.Name,
					Args: args,
				})
			}

			if !emitChunk(ctx, ch, chunk) {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			emitChunk(ctx, ch, StreamChunk{Err: err, Done: true})
		}
	}()

	return ch, nil
}
