package provider

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RetryTransport wraps an http.RoundTripper with retry logic for transient errors.
type RetryTransport struct {
	Base         http.RoundTripper
	MaxRetries   int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

// NewRetryTransport creates a new RetryTransport with sensible defaults.
func NewRetryTransport(base http.RoundTripper) *RetryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &RetryTransport{
		Base:         base,
		MaxRetries:   3,
		InitialDelay: 500 * time.Millisecond,
		MaxDelay:     30 * time.Second,
	}
}

// RoundTrip retries 429/500/502/503/504 for idempotent methods only.
// POSTs are never replayed: a failed POST may already have been accepted and billed upstream.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}

	retryable := isIdempotentMethod(req.Method)

	maxRetries := t.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	for attempt := 0; ; attempt++ {
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := t.Base.RoundTrip(req)

		shouldRetry := false
		var retryDelay time.Duration

		if err != nil {
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			shouldRetry = retryable
		} else if retryable && isRetryableStatus(resp.StatusCode) {
			shouldRetry = true
			retryDelay = parseRetryAfter(resp.Header.Get("Retry-After"))
			if t.MaxDelay > 0 && retryDelay > t.MaxDelay {
				// Retry-After is untrusted; clamp so a broken server can't park the turn.
				retryDelay = t.MaxDelay
			}
			if attempt < maxRetries {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				resp = nil
			} else {
				// Buffer final body so the caller gets a readable error instead of a drained stream.
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRetryBodyBytes))
				resp.Body.Close()
				if readErr != nil {
					return nil, readErr
				}
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}
		}

		if !shouldRetry || attempt >= maxRetries {
			return resp, err
		}

		if retryDelay <= 0 {
			backoff := t.InitialDelay * (1 << attempt)
			jitter := time.Duration(float64(backoff) * (0.1 + rand.Float64()*0.1))
			retryDelay = backoff + jitter
			if t.MaxDelay > 0 && retryDelay > t.MaxDelay {
				retryDelay = t.MaxDelay
			}
		}

		select {
		case <-time.After(retryDelay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}

func isIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// maxRetryBodyBytes caps the buffered final error body.
const maxRetryBodyBytes = 1 << 20

func isRetryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		diff := time.Until(t)
		if diff > 0 {
			return diff
		}
	}
	return 0
}
